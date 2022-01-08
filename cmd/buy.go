package cmd

import (
	"fmt"
	"os"
	"sync"
	"syscall"
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
		wsEventBroker := broker.New(&logger)
		accEventBroker := broker.New(&logger)
		st := storage.NewStorage()
		wsSvc := service.NewWS(cfg, wsEventBroker, &logger)
		orderSvc := service.NewOrder(cfg, st, wsEventBroker, wsSvc, &logger)
		balanceSvc := service.NewBalance(st, wsEventBroker, accEventBroker, wsSvc, &logger)
		sessSvc := buySessSvc.New(st)

		cmdManager := service.NewManager(&logger)

		ctx, interrupt := cmdManager.ListenSignal()

		tgSvc, err := service.NewTelegram(cfg, &logger)
		if err != nil {
			logger.Err(err).Msg("new tg")

			interrupt <- syscall.SIGINT

			return
		}

		go tgSvc.Start()

		tgSvc.Send(fmt.Sprintf("env: %s, buy bot starting", cfg.Env))
		defer tgSvc.SendSync(fmt.Sprintf("env: %s, buy bot shutting down", cfg.Env))

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

		for {
			if st.GetPair(cfg.Spread.Pair) != nil {
				break
			}

			time.Sleep(pairsWaitingDuration)
		}

		wg.Add(1)
		go accounting.Start(ctx, interrupt, &wg)

		pair := st.GetPair(cfg.Buy.Pair)
		if pair == nil {
			logger.
				Err(dictionary.ErrInvalidPair).
				Str("pair", cfg.Buy.Pair).
				Msg(dictionary.ErrInvalidPair.Error())

			interrupt <- syscall.SIGINT
		}

		orderBookEventBroker := broker.New(&logger)
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
			interrupt <- syscall.SIGINT
		} else {
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
	},
}
