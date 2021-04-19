package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
)

const (
	ShutDownDuration = time.Second * 10
)

//nolint: gochecknoglobals
var startCmd = &cobra.Command{
	Use:   "bot",
	Short: "Start trade bot for kickex exchange",
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

		time.Sleep(time.Second) // wait for pairs filling

		go eventBroker.Start()
		go accounting.Start(ctx, interrupt)

		var wg sync.WaitGroup

		for _, pairName := range cfg.Pairs {
			pair := st.GetPair(pairName)
			if pair == nil {
				logger.Err(dictionary.ErrInvalidPair).Msg(dictionary.ErrInvalidPair.Error())

				cancel()

				break
			}

			st.RegisterOrderBook(pair)

			go subscriber.NewOrderBook(cfg, st, eventBroker, pair, &logger).Start(ctx, interrupt)

			orderManager, err := subscriber.NewOrder(cfg, st, eventBroker, pair, &logger)
			if err != nil {
				cancel()

				break
			}

			wg.Add(1)
			go orderManager.Start(ctx, &wg, interrupt)
		}

		go func() {
			for {
				select {
				case <-time.After(time.Minute):
					logger.Info().Msg("run cleanup old orders")

					st.CleanUpOldOrders()
				case <-ctx.Done():
					return
				}
			}
		}()

		go func() {
			<-interrupt

			cancel()
		}()

		wg.Wait()

		logger.Warn().Msg("shutting down...")
	},
}
