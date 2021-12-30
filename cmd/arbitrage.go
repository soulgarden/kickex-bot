package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	spreadSessSvc "github.com/soulgarden/kickex-bot/service/spread"

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

const pairsWaitingDuration = time.Millisecond * 100

//nolint: gochecknoglobals
var arbitrageCmd = &cobra.Command{
	Use:   "arbitrage",
	Short: "Start bot for kickex exchange that search price arbitrage in pairs",
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
		balanceSvc := service.NewBalance(st, wsEventBroker, accEventBroker, wsSvc, &logger)
		orderSvc := service.NewOrder(cfg, st, wsEventBroker, wsSvc, &logger)
		sessSvc := spreadSessSvc.New(st)

		interrupt := make(chan os.Signal, interruptChSize)
		signal.Notify(interrupt, os.Interrupt)
		signal.Notify(interrupt, syscall.SIGTERM)

		ctx, cancel := context.WithCancel(context.Background())

		logger.Warn().Msg("starting...")

		err := wsSvc.Connect(interrupt)
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

		accountingSub := subscriber.NewAccounting(cfg, st, wsEventBroker, accEventBroker, wsSvc, balanceSvc, &logger)
		pairsSub := subscriber.NewPairs(cfg, st, wsEventBroker, wsSvc, &logger)

		var wg sync.WaitGroup

		wg.Add(1)

		go pairsSub.Start(ctx, interrupt, &wg)

		tgSvc, err := service.NewTelegram(cfg, &logger)
		if err != nil {
			logger.Err(err).Msg("new tg")

			cancel()

			return
		}

		go tgSvc.Start()

		arb := strategy.NewArbitrage(
			cfg,
			st,
			service.NewConversion(st, &logger),
			tgSvc,
			wsSvc,
			wsEventBroker,
			accEventBroker,
			orderSvc,
			sessSvc,
			balanceSvc,
			&logger,
		)

		pairs := arb.GetPairsList()

		for {
			pairsNum := len(pairs)
			for pairName := range pairs {
				if st.GetPair(pairName) != nil {
					pairsNum--
				}
			}

			if pairsNum == 0 {
				break
			}

			time.Sleep(pairsWaitingDuration)
		}

		tgSvc.Send(fmt.Sprintf("env: %s, arbitrage bot starting", cfg.Env))

		wg.Add(1)
		go accountingSub.Start(ctx, interrupt, &wg)

		for pairName := range pairs {
			pair := st.GetPair(pairName)
			if pair == nil {
				logger.
					Err(dictionary.ErrInvalidPair).
					Str("pair", pairName).
					Msg(dictionary.ErrInvalidPair.Error())

				cancel()

				break
			}

			orderBookEventBroker := broker.New(&logger)
			go orderBookEventBroker.Start()

			orderBook := st.RegisterOrderBook(pair, orderBookEventBroker)

			wg.Add(1)
			go subscriber.NewOrderBook(cfg, st, wsEventBroker, wsSvc, pair, orderBook, &logger).Start(ctx, &wg, interrupt)
		}

		time.Sleep(pairsWaitingDuration)

		wg.Add(1)
		go arb.Start(ctx, &wg, interrupt)

		go func() {
			<-interrupt

			tgSvc.SendSync(fmt.Sprintf("env: %s, arbitrage bot shutting down", cfg.Env))

			cancel()

			<-time.After(ShutDownDuration)

			logger.Error().Msg("killed by shutdown timeout")

			os.Exit(1)
		}()

		wg.Wait()

		wsSvc.Close()

		logger.Warn().Msg("shutting down...")
	},
}
