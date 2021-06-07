package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	buySessSvc "github.com/soulgarden/kickex-bot/service/buy"

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

//nolint: gochecknoglobals
var buyCmd = &cobra.Command{
	Use:   "buy",
	Short: "Start buy bot for kickex exchange",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := conf.New()

		defaultLogLevel := zerolog.InfoLevel
		if cfg.Debug {
			defaultLogLevel = zerolog.DebugLevel
		}

		logger := zerolog.New(os.Stdout).Level(defaultLogLevel).With().Caller().Logger()
		wsEventBroker := broker.New()
		accEventBroker := broker.New()
		st := storage.NewStorage()
		wsSvc := service.NewWS(cfg, wsEventBroker, &logger)
		orderSvc := service.NewOrder(cfg, st, wsEventBroker, wsSvc, &logger)
		balanceSvc := service.NewBalance(st, wsEventBroker, wsSvc, &logger)
		sessSvc := buySessSvc.New(st)

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
		go accEventBroker.Start()

		logger.Warn().Msg("starting...")

		err = balanceSvc.GetBalance(ctx, interrupt)
		if err != nil {
			logger.Fatal().Err(err).Msg("get balance")
		}

		accounting := subscriber.NewAccounting(cfg, st, wsEventBroker, accEventBroker, wsSvc, balanceSvc, &logger)
		pairs := subscriber.NewPairs(cfg, st, wsEventBroker, wsSvc, &logger)

		var wg sync.WaitGroup

		wg.Add(1)
		go pairs.Start(ctx, interrupt, &wg)

		time.Sleep(pairsWaitingDuration) // wait for pairs filling

		wg.Add(1)
		go accounting.Start(ctx, interrupt, &wg)

		for _, pairName := range cfg.Buy.Pairs {
			pair := st.GetPair(pairName)
			if pair == nil {
				logger.
					Err(dictionary.ErrInvalidPair).
					Str("pair", pairName).
					Msg(dictionary.ErrInvalidPair.Error())

				cancel()

				break
			}

			orderBookEventBroker := broker.New()
			go orderBookEventBroker.Start()

			orderBook := st.RegisterOrderBook(pair, orderBookEventBroker)

			wg.Add(1)
			go subscriber.NewOrderBook(cfg, st, wsEventBroker, wsSvc, pair, orderBook, &logger).Start(ctx, &wg, interrupt)

			buyStrategy, err := strategy.NewBuy(
				cfg,
				st,
				wsEventBroker,
				accEventBroker,
				service.NewConversion(st, &logger),
				tgSvc,
				wsSvc,
				pair,
				orderBook,
				orderSvc,
				sessSvc,
				&logger,
			)
			if err != nil {
				cancel()

				break
			}

			wg.Add(1)
			go buyStrategy.Start(ctx, &wg, interrupt)
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

		logger.Warn().Msg("shutting down...")
	},
}
