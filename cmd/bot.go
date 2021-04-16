package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
)

const (
	ShutDownDuration = time.Second * 5
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

		orderManager, err := subscriber.NewOrder(cfg, st, eventBroker, &logger)
		if err != nil {
			cancel()
			os.Exit(1)
		}

		orderBook, err := subscriber.NewOrderBook(cfg, st, eventBroker, &logger)
		if err != nil {
			cancel()
			os.Exit(1)
		}

		accounting := subscriber.NewAccounting(cfg, st, eventBroker, &logger)

		go eventBroker.Start(ctx)

		go func() {
			err := orderBook.Start(ctx, interrupt)
			if err != nil {
				interrupt <- syscall.SIGINT
			}

			log.Err(err).Msg("stop subscribe order book")
		}()
		go func() {
			err := accounting.Start(ctx, interrupt)
			if err != nil {
				interrupt <- syscall.SIGINT
			}

			log.Err(err).Msg("stop subscribe acc")
		}()
		go func() {
			err := orderManager.Start(ctx, interrupt)
			if err != nil {
				interrupt <- syscall.SIGINT
			}
			log.Err(err).Msg("stop order manager")
		}()

		<-interrupt

		logger.Warn().Msg("shutting down...")

		cancel()

		<-time.After(ShutDownDuration)
	},
}
