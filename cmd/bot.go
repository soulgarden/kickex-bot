package cmd

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
)

const (
	eventChSize      = 1024
	ShutDownDuration = time.Second * 3
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

		eventCh := make(chan int, eventChSize)

		st := storage.NewStorage()

		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)

		ctx, cancel := context.WithCancel(context.Background())

		orderManager := subscriber.NewOrder(cfg, st, &logger)

		go func() {
			log.Err(subscriber.SubscribeOrderBook(ctx, cfg, st, eventCh, &logger)).Msg("stop subscribe order book")
		}()
		go func() {
			log.Err(subscriber.SubscribeAccounting(ctx, cfg, st, eventCh, &logger)).Msg("stop subscribe acc")
		}()
		go func() {
			log.Err(orderManager.Start(ctx, eventCh)).Msg("stop order manager")
		}()

		<-interrupt

		cancel()

		<-time.After(ShutDownDuration)
	},
}
