package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/soulgarden/kickex-bot/strategy"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/service"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
)

const (
	ShutDownDuration = time.Second * 15
	cleanupInterval  = time.Minute * 10
	interruptChSize  = 100
)

//nolint: gochecknoglobals
var spreadCmd = &cobra.Command{
	Use:   "spread",
	Short: "Start spread trade bot for kickex exchange ",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := conf.New()

		defaultLogLevel := zerolog.InfoLevel
		if cfg.Debug {
			defaultLogLevel = zerolog.DebugLevel
		}

		logger := zerolog.New(os.Stdout).Level(defaultLogLevel).With().Caller().Logger()
		wsEventBroker := broker.NewBroker()
		st := storage.NewStorage()
		wsSvc := service.NewWS(cfg, wsEventBroker, &logger)
		orderSvc := service.NewOrder(cfg, st, wsEventBroker, wsSvc, &logger)
		balanceSvc := service.NewBalance(st, wsEventBroker, wsSvc, &logger)

		interrupt := make(chan os.Signal, interruptChSize)
		signal.Notify(interrupt, os.Interrupt)

		ctx, cancel := context.WithCancel(context.Background())

		logger.Warn().Msg("starting...")

		tgSvc, err := service.NewTelegram(cfg, &logger)
		if err != nil {
			logger.Err(err).Msg("new tg")

			cancel()

			return
		}

		go tgSvc.Start()

		tgSvc.Send(fmt.Sprintf("env: %s, spread trading bot starting", cfg.Env))
		defer tgSvc.SendSync(fmt.Sprintf("env: %s, spread trading bot shutting down", cfg.Env))

		go func() {
			<-interrupt

			logger.Warn().Msg("interrupt signal received")

			cancel()

			<-time.After(ShutDownDuration)

			logger.Warn().Msg("killed by shutdown timeout")

			os.Exit(1)
		}()

		err = wsSvc.Connect(interrupt)
		if err != nil {
			logger.Err(err).Msg("ws connect")

			os.Exit(1)
		}

		go wsSvc.Start(ctx, interrupt)
		go wsEventBroker.Start()

		_, err = os.Stat(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
		if err == nil {
			logger.Warn().Msg("load sessions from dump file")

			err = st.LoadSessions(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
			if err != nil {
				logger.Fatal().Err(err).Msg("load sessions from dump")
			}

			e := os.Remove(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
			if e != nil {
				logger.Fatal().Err(err).Msg("remove dump file")
			}

			if len(st.GetUserOrders()) > 0 {
				err = orderSvc.UpdateOrdersStates(ctx, interrupt)
				if err != nil {
					logger.Fatal().Err(err).Msg("update orders for dumped state")
				}
			}
		} else if !os.IsNotExist(err) {
			logger.Fatal().Err(err).Msg("open dump file")
		}

		logger.Warn().Msg("starting...")

		err = balanceSvc.GetBalance(ctx, interrupt)
		if err != nil {
			logger.Fatal().Err(err).Msg("get balance")
		}

		accounting := subscriber.NewAccounting(cfg, st, wsEventBroker, wsSvc, balanceSvc, &logger)
		pairs := subscriber.NewPairs(cfg, st, wsEventBroker, wsSvc, &logger)

		var wg sync.WaitGroup

		wg.Add(1)
		go pairs.Start(ctx, interrupt, &wg)

		time.Sleep(pairsWaitingDuration) // wait for pairs filling

		wg.Add(1)
		go accounting.Start(ctx, interrupt, &wg)

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

			orderBookEventBroker := broker.NewBroker()
			go orderBookEventBroker.Start()

			orderBook := st.RegisterOrderBook(pair, orderBookEventBroker)

			wg.Add(1)
			go subscriber.NewOrderBook(cfg, st, wsEventBroker, wsSvc, pair, orderBook, &logger).Start(ctx, &wg, interrupt)

			spreadTrader, err := strategy.NewSpread(
				cfg,
				st,
				wsEventBroker,
				service.NewConversion(st, &logger),
				tgSvc,
				wsSvc,
				pair,
				orderBook,
				orderSvc,
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

		wg.Wait()

		wsSvc.Close()

		err = st.DumpSessions(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
		logger.Err(err).Msg("dump sessions to file")

		logger.Warn().Msg("shutting down...")
	},
}
