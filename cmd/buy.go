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
	buySessSvc "github.com/soulgarden/kickex-bot/service/buy"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/strategy"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	tb "gopkg.in/tucnak/telebot.v2"
)

func newBuyCmd() *cobra.Command {
	return &cobra.Command{
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

			tgSvc.SendAsync(fmt.Sprintf("env: %s, buy bot starting", cfg.Env))
			defer tgSvc.SendSync(fmt.Sprintf("env: %s, buy bot shutting down", cfg.Env))

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

			for {
				if st.GetPair(cfg.Spread.Pair) != nil {
					break
				}

				time.Sleep(pairsWaitingDuration)
			}

			g.Go(func() error { return accountingSub.Start(ctx) })

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

			g.Go(func() error {
				return subscriber.NewOrderBook(cfg, st, wsEventBroker, wsSvc, pair, orderBook, &logger).Start(ctx)
			})

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
				g.Go(func() error { return buyStrategy.Start(ctx, g) })
			}

			go func() {
				ticker := time.NewTicker(cleanupInterval)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						logger.Info().Msg("run cleanup old orders")

						st.CleanUpOldOrders()
					case <-ctx.Done():
						return
					}
				}
			}()

			err = g.Wait()

			logger.Err(err).Msg("wait goroutines")

			wsSvc.Close()
		},
	}
}
