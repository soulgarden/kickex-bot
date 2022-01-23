package cmd

import (
	"fmt"
	"math/big"
	"os"
	"syscall"
	"time"

	tb "gopkg.in/tucnak/telebot.v2"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	spreadSessSvc "github.com/soulgarden/kickex-bot/service/spread"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/strategy"
	"github.com/soulgarden/kickex-bot/subscriber"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

const (
	cleanupInterval = time.Minute * 5
)

func newSpreadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "spread",
		Short: "Start spread trade bot for kickex exchange",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cfg := conf.New()

			defaultLogLevel := zerolog.InfoLevel
			if cfg.Debug {
				defaultLogLevel = zerolog.DebugLevel
			}

			logger := zerolog.New(os.Stdout).Level(defaultLogLevel).With().Timestamp().Caller().Logger()

			wsEventBroker := broker.New(&logger)
			accEventBroker := broker.New(&logger)
			forceCheckBroker := broker.New(&logger)

			st := storage.NewStorage()
			wsSvc := service.NewWS(cfg, wsEventBroker, &logger)
			orderSvc := service.NewOrder(cfg, st, wsEventBroker, wsSvc, &logger)
			balanceSvc := service.NewBalance(st, wsEventBroker, accEventBroker, wsSvc, &logger)
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

			tgSvc.SendAsync(fmt.Sprintf("env: %s, spread trading bot starting", cfg.Env))
			defer tgSvc.SendSync(fmt.Sprintf("env: %s, spread trading bot shutting down", cfg.Env))

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
			go forceCheckBroker.Start()

			g.Go(func() error { return balanceSvc.GetBalance(ctx) })

			dumpSvc := spreadSessSvc.NewDump(cfg, orderSvc, &logger)

			isExists, err := dumpSvc.IsStateFileExists()
			if err != nil {
				interrupt <- syscall.SIGINT

				return
			}

			if isExists {
				err = dumpSvc.LoadFromStateFile(ctx, st)
				if err != nil {
					interrupt <- syscall.SIGINT

					return
				}
			}

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

			pair := st.GetPair(cfg.Spread.Pair)
			if pair == nil {
				logger.
					Err(dictionary.ErrInvalidPair).
					Str("pair", cfg.Spread.Pair).
					Msg(dictionary.ErrInvalidPair.Error())

				interrupt <- syscall.SIGINT

				return
			}

			orderBookEventBroker := broker.New(&logger)
			go orderBookEventBroker.Start()

			orderBook := st.RegisterOrderBook(pair, orderBookEventBroker)

			zeroStep := big.NewFloat(0).Text('f', pair.PriceScale)
			priceStepStr := zeroStep[0:pair.PriceScale+1] + "1"

			priceStep, ok := big.NewFloat(0).SetPrec(uint(pair.PriceScale)).SetString(priceStepStr)
			if !ok {
				logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

				interrupt <- syscall.SIGINT

				return
			}

			g.Go(func() error {
				return subscriber.NewOrderBook(cfg, st, wsEventBroker, wsSvc, pair, orderBook, &logger).Start(ctx)
			})

			spreadTGSvc := spreadSessSvc.NewTg(cfg, pair, tgSvc, sessSvc, orderBook)

			spreadOrderSvc, err := spreadSessSvc.NewOrder(
				cfg,
				pair,
				orderBook,
				st,
				wsSvc,
				orderSvc,
				sessSvc,
				spreadTGSvc,
				priceStep,
				forceCheckBroker,
				&logger,
			)
			if err != nil {
				logger.Err(err).Msg("new spread order svc")

				interrupt <- syscall.SIGINT

				return
			}

			decideSvc, err := spreadSessSvc.NewDecider(
				cfg,
				pair,
				orderBook,
				st,
				spreadOrderSvc,
				spreadTGSvc,
				forceCheckBroker,
				&logger,
			)
			if err != nil {
				logger.Err(err).Msg("new decide svc")

				interrupt <- syscall.SIGINT

				return
			}

			watchSvc, err := spreadSessSvc.NewWatch(
				cfg,
				pair,
				orderBook,
				st,
				accEventBroker,
				orderSvc,
				spreadOrderSvc,
				decideSvc,
				priceStep,
				forceCheckBroker,
				&logger,
			)
			if err != nil {
				logger.Err(err).Msg("new spread watch svc")

				interrupt <- syscall.SIGINT

				return
			}

			spreadTrader, err := strategy.NewSpread(
				cfg,
				st,
				wsEventBroker,
				service.NewConversion(st, &logger),
				wsSvc,
				pair,
				orderBook,
				orderSvc,
				watchSvc,
				decideSvc,
				spreadOrderSvc,
				spreadTGSvc,
				forceCheckBroker,
				&logger,
			)
			if err != nil {
				interrupt <- syscall.SIGINT

				return
			}

			g.Go(func() error { return spreadTrader.Start(ctx, g) })

			go func() {
				ticker := time.NewTicker(cleanupInterval)
				defer ticker.Stop()

				for {
					select {
					case <-ticker.C:
						logger.Info().Int("deleted", st.CleanUpOldOrders()).Msg("run cleanup old orders")
					case <-ctx.Done():
						return
					}
				}
			}()

			err = g.Wait()

			logger.Err(err).Msg("wait goroutines")

			wsSvc.Close()

			d := storage.NewDumpStorage(st)
			err = d.DumpStorage(fmt.Sprintf(cfg.StorageDumpPath, cfg.Env))
			logger.Err(err).Msg("dump sessions to file")
		},
	}
}
