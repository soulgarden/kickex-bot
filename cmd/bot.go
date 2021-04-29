package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/soulgarden/kickex-bot/service"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
)

const (
	ShutDownDuration = time.Second * 10
	cleanupInterval  = time.Minute * 10
)

//nolint: gochecknoglobals
var startCmd = &cobra.Command{
	Use:   "bot",
	Short: "Start spread trade bot for kickex exchange ",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := conf.New()

		defaultLogLevel := zerolog.InfoLevel
		if cfg.Debug {
			defaultLogLevel = zerolog.DebugLevel
		}

		logger := zerolog.New(os.Stdout).Level(defaultLogLevel).With().Caller().Logger()
		eventBroker := subscriber.NewBroker()
		st := storage.NewStorage()
		orderSvc := service.NewOrder(cfg, st, &logger)

		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)

		ctx, cancel := context.WithCancel(context.Background())

		_, err := os.Stat(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
		if err == nil {
			logger.Warn().Msg("load sessions from dump file")

			err = st.LoadSessions(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
			if err != nil {
				logger.Fatal().Err(err).Msg("load sessions from dump")
			}

			if len(st.UserOrders) > 0 {
				err = orderSvc.UpdateOrderStates(ctx, interrupt)
				if err != nil {
					logger.Fatal().Err(err).Msg("update orders for dumped state")
				}
			}

		} else if !os.IsNotExist(err) {
			logger.Fatal().Err(err).Msg("open dump file")
		}

		logger.Warn().Msg("starting...")

		accounting := subscriber.NewAccounting(cfg, st, eventBroker, &logger)
		pairs := subscriber.NewPairs(cfg, st, &logger)

		go pairs.Start(ctx, interrupt)

		time.Sleep(pairsWaitingDuration) // wait for pairs filling

		tgSvc, err := service.NewTelegram(cfg, &logger)
		if err != nil {
			logger.Err(err).Msg("new tg")

			cancel()

			return
		}

		tgSvc.Send(fmt.Sprintf("env: %s, spread trading bot starting", cfg.Env))

		go tgSvc.Start()
		go eventBroker.Start()
		go accounting.Start(ctx, interrupt)

		var wg sync.WaitGroup

		for _, pairName := range cfg.Pairs {
			pair := st.GetPair(pairName)
			if pair == nil {
				logger.
					Err(dictionary.ErrInvalidPair).
					Str("pair", pairName).
					Msg(dictionary.ErrInvalidPair.Error())

				cancel()

				break
			}

			st.RegisterOrderBook(pair)

			go subscriber.NewOrderBook(cfg, st, eventBroker, pair, &logger).Start(ctx, interrupt)

			spreadTrader, err := subscriber.NewSpread(
				cfg,
				st,
				eventBroker,
				service.NewConversion(st, &logger),
				tgSvc,
				pair,
				&logger,
			)
			if err != nil {
				cancel()

				break
			}

			wg.Add(1)
			go spreadTrader.Start(ctx, &wg, interrupt)
		}

		go func() {
			for {
				select {
				case <-time.After(cleanupInterval):
					logger.Info().Msg("run cleanup old orders")

					st.CleanUpOldOrders()
				case <-ctx.Done():
					return
				}
			}
		}()

		go func() {
			<-interrupt

			tgSvc.SendSync(fmt.Sprintf("env: %s, spread trading bot shutting down", cfg.Env))

			cancel()

			<-time.After(ShutDownDuration)

			os.Exit(1)
		}()

		wg.Wait()

		err = st.DumpSessions(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
		logger.Err(err).Msg("dump sessions to file")

		logger.Warn().Msg("shutting down...")
	},
}
