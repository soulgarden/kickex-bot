package cmd

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/service/arbitrage"
	spreadSessSvc "github.com/soulgarden/kickex-bot/service/spread"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/strategy"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	tb "gopkg.in/tucnak/telebot.v2"
)

const pairsWaitingDuration = time.Millisecond * 100

func NewArbitrageCmd() *cobra.Command {
	return &cobra.Command{
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

			tgBot, err := tb.NewBot(tb.Settings{
				Token: cfg.Telegram.Token,
			})
			if err != nil {
				logger.Err(err).Msg("new tg bot")

				return
			}

			cmdManager := service.NewManager(&logger)

			ctx, interrupt := cmdManager.ListenSignal()

			g, ctx := errgroup.WithContext(ctx)

			tgSvc := service.NewTelegram(cfg, tgBot, &logger)

			go tgSvc.Start()

			tgSvc.SendAsync(fmt.Sprintf("env: %s, arbitrage bot starting", cfg.Env))
			defer tgSvc.SendSync(fmt.Sprintf("env: %s, arbitrage bot shutting down", cfg.Env))

			wsG, _ := errgroup.WithContext(ctx)

			if err := wsSvc.Connect(wsG); err != nil {
				interrupt <- syscall.SIGINT

				return
			}
			g.Go(func() error { return wsSvc.Start(ctx) })

			go func() {
				err := wsG.Wait()
				if err != nil {
					interrupt <- syscall.SIGINT
				}
			}()

			go wsEventBroker.Start()
			go accEventBroker.Start()

			g.Go(func() error { return balanceSvc.GetBalance(ctx) })

			accountingSub := subscriber.NewAccounting(cfg, st, wsEventBroker, accEventBroker, wsSvc, balanceSvc, &logger)
			pairsSub := subscriber.NewPairs(cfg, st, wsEventBroker, wsSvc, &logger)

			g.Go(func() error { return pairsSub.Start(ctx) })

			arb, err := strategy.NewArbitrage(
				cfg,
				st,
				service.NewConversion(st, &logger),
				arbitrage.NewTg(cfg, tgSvc),
				wsSvc,
				wsEventBroker,
				accEventBroker,
				orderSvc,
				sessSvc,
				balanceSvc,
				&logger,
			)
			if err != nil {
				logger.Err(err).Msg("new arbitrage")

				interrupt <- syscall.SIGINT

				return
			}

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

			g.Go(func() error { return accountingSub.Start(ctx) })

			for pairName := range pairs {
				pair := st.GetPair(pairName)
				if pair == nil {
					logger.
						Err(dictionary.ErrInvalidPair).
						Str("pair", pairName).
						Msg(dictionary.ErrInvalidPair.Error())

					interrupt <- syscall.SIGINT

					break
				}

				orderBookEventBroker := broker.New(&logger)
				go orderBookEventBroker.Start()

				orderBook := st.RegisterOrderBook(pair, orderBookEventBroker)

				g.Go(func() error {
					return subscriber.NewOrderBook(cfg, st, wsEventBroker, wsSvc, pair, orderBook, &logger).Start(ctx)
				})
			}

			g.Go(func() error { return arb.Start(ctx, g) })

			err = g.Wait()

			logger.Err(err).Msg("wait goroutines")

			wsSvc.Close()
		},
	}
}
