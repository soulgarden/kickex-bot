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

	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
)

const pairsWaitingDuration = 5 * time.Second

//nolint: gochecknoglobals
var disbalanceCmd = &cobra.Command{
	Use:   "disbalance",
	Short: "Start bot for kickex exchange that search price disbalance in pairs",
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
		balanceSvc := service.NewBalance(st, wsEventBroker, wsSvc, &logger)

		interrupt := make(chan os.Signal, interruptChSize)
		signal.Notify(interrupt, os.Interrupt)

		ctx, cancel := context.WithCancel(context.Background())

		logger.Warn().Msg("starting...")

		err := wsSvc.Connect(interrupt)
		if err != nil {
			logger.Err(err).Msg("ws connect")

			os.Exit(1)
		}

		go wsSvc.Start(ctx, interrupt)
		go wsEventBroker.Start()

		accounting := subscriber.NewAccounting(cfg, st, wsEventBroker, wsSvc, balanceSvc, &logger)
		pairs := subscriber.NewPairs(cfg, st, wsEventBroker, wsSvc, &logger)

		var wg sync.WaitGroup

		wg.Add(1)

		go pairs.Start(ctx, interrupt, &wg)

		time.Sleep(pairsWaitingDuration) // wait for pairs filling

		tgSvc, err := service.NewTelegram(cfg, &logger)
		if err != nil {
			logger.Err(err).Msg("new tg")

			cancel()

			return
		}

		go tgSvc.Start()

		tgSvc.Send(fmt.Sprintf("env: %s, disbalance bot starting", cfg.Env))

		wg.Add(1)
		go accounting.Start(ctx, interrupt, &wg)

		dis := strategy.NewDisbalance(cfg, st, wsEventBroker, service.NewConversion(st, &logger), tgSvc, &logger)

		for _, pairName := range cfg.TrackingPairs {
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
		}

		time.Sleep(pairsWaitingDuration)

		wg.Add(1)
		go dis.Start(ctx, &wg, interrupt)

		go func() {
			<-interrupt

			tgSvc.SendSync(fmt.Sprintf("env: %s, disbalance bot shutting down", cfg.Env))

			cancel()

			<-time.After(ShutDownDuration)

			os.Exit(1)
		}()

		wg.Wait()

		wsSvc.Close()

		logger.Warn().Msg("shutting down...")
	},
}
