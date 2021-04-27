package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

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
		eventBroker := subscriber.NewBroker()
		st := storage.NewStorage()

		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)

		ctx, cancel := context.WithCancel(context.Background())

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

		tgSvc.Send(fmt.Sprintf("env: %s, disbalance bot starting", cfg.Env))

		go tgSvc.Start()
		go eventBroker.Start()
		go accounting.Start(ctx, interrupt)

		var wg sync.WaitGroup
		dis := subscriber.NewDisbalance(cfg, st, eventBroker, service.NewConversion(st, &logger), tgSvc, &logger)

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

			st.RegisterOrderBook(pair)

			go subscriber.NewOrderBook(cfg, st, eventBroker, pair, &logger).Start(ctx, interrupt)
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

		logger.Warn().Msg("shutting down...")
	},
}
