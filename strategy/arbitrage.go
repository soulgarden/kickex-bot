package strategy

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

const sendInterval = 300 * time.Second
const spreadForAlert = 2.7

type Arbitrage struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *broker.Broker
	conversion  *service.Conversion
	tgSvc       *service.Telegram
	sentAt      *time.Time
	logger      *zerolog.Logger
}

func New(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	conversion *service.Conversion,
	tgSvc *service.Telegram,
	logger *zerolog.Logger,
) *Arbitrage {
	return &Arbitrage{
		cfg:         cfg,
		storage:     storage,
		eventBroker: eventBroker,
		conversion:  conversion,
		tgSvc:       tgSvc,
		logger:      logger,
	}
}

func (s *Arbitrage) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan os.Signal) {
	defer wg.Done()

	s.logger.Warn().Msg("arbitrage process started")
	defer s.logger.Warn().Msg("stop arbitrage process")

	ch := make(chan bool, 1024)

	go s.collectEvents(ctx, interrupt, ch)

	pairs := s.GetArbitragePairsAsMap()

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGSTOP

				return
			}

			s.check(pairs)

		case <-ctx.Done():
			return
		}
	}
}

func (s *Arbitrage) collectEvents(ctx context.Context, interrupt chan os.Signal, ch chan<- bool) {
	var wg sync.WaitGroup

	s.logger.Warn().Msg("start collect events")
	defer s.logger.Warn().Msg("finish collect events")

	pairs := s.GetPairsList()

	for pairName := range pairs {
		pair := s.storage.GetPair(pairName)

		wg.Add(1)

		go s.listenEvents(ctx, &wg, interrupt, pair, ch)
	}

	wg.Wait()
}

func (s *Arbitrage) listenEvents(
	ctx context.Context,
	wg *sync.WaitGroup,
	interrupt chan os.Signal,
	pair *storage.Pair,
	ch chan<- bool,
) {
	defer wg.Done()

	s.logger.Warn().Str("pair", pair.GetPairName()).Msg("start listen events")
	defer s.logger.Warn().Str("pair", pair.GetPairName()).Msg("finish listen events")

	e := s.storage.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].EventBroker.Subscribe()
	defer s.storage.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].EventBroker.Unsubscribe(e)

	for {
		select {
		case _, ok := <-e:
			if !ok {
				s.logger.Warn().Msg("receive event error")

				interrupt <- syscall.SIGSTOP

				return
			}

			ch <- true
		case <-ctx.Done():
			return
		}
	}
}

func (s *Arbitrage) check(pairs map[string]map[string]bool) {
	for baseCurrency, quotedCurrencies := range pairs {
		for quotedCurrency := range quotedCurrencies {
			baseUSDTPair := s.storage.GetPair(baseCurrency + "/" + dictionary.USDT)

			baseBuyOrder := s.storage.OrderBooks[baseUSDTPair.BaseCurrency][baseUSDTPair.QuoteCurrency].GetMinAsk()
			baseSellOrder := s.storage.OrderBooks[baseUSDTPair.BaseCurrency][baseUSDTPair.QuoteCurrency].GetMaxBid()

			quotedUSDTPair := s.storage.GetPair(quotedCurrency + "/" + dictionary.USDT)
			quotedBuyOrder := s.storage.OrderBooks[quotedUSDTPair.BaseCurrency][quotedUSDTPair.QuoteCurrency].GetMinAsk()
			quotedSellOrder := s.storage.OrderBooks[quotedUSDTPair.BaseCurrency][quotedUSDTPair.QuoteCurrency].GetMaxBid()

			book := s.storage.OrderBooks[baseCurrency][quotedCurrency]
			baseQuotedPair := s.storage.GetPair(baseCurrency + "/" + quotedCurrency)
			baseQuotedBuyOrder := book.GetMinAsk()
			baseQuotedSellOrder := book.GetMaxBid()

			if baseBuyOrder == nil || quotedSellOrder == nil || baseQuotedSellOrder == nil {
				continue
			}

			s.checkBuyBaseOption(
				baseUSDTPair,
				baseBuyOrder,
				quotedUSDTPair,
				quotedSellOrder,
				baseQuotedPair,
				baseQuotedSellOrder,
			)

			s.checkBuyQuotedOptions(
				baseUSDTPair,
				quotedBuyOrder,
				quotedUSDTPair,
				baseSellOrder,
				baseQuotedPair,
				baseQuotedBuyOrder,
			)
		}
	}
}

func (s *Arbitrage) GetPairsList() map[string]bool {
	pairs := make(map[string]bool)

	for _, pairName := range s.cfg.ArbitragePairs {
		pair := strings.Split(pairName, "/")

		pairs[pair[0]+"/"+pair[1]] = true

		pairs[pair[0]+"/"+dictionary.USDT] = true
		pairs[pair[1]+"/"+dictionary.USDT] = true
	}

	return pairs
}

func (s *Arbitrage) GetArbitragePairsAsMap() map[string]map[string]bool {
	pairs := make(map[string]map[string]bool)

	for _, pairName := range s.cfg.ArbitragePairs {
		pair := s.storage.GetPair(pairName)

		if _, ok := pairs[pair.BaseCurrency]; !ok {
			pairs[pair.BaseCurrency] = make(map[string]bool)
		}

		pairs[pair.BaseCurrency][pair.QuoteCurrency] = false
	}

	return pairs
}

func (s *Arbitrage) checkBuyBaseOption(
	baseUSDTPair *storage.Pair,
	baseBuyOrder *storage.BookOrder,
	quotedUSDTPair *storage.Pair,
	quotedSellOrder *storage.BookOrder,
	baseQuotedPair *storage.Pair,
	baseQuotedSellOrder *storage.BookOrder,
) {
	s.logger.Debug().
		Str("pair", baseQuotedPair.GetPairName()).
		Str("base buy order total", baseBuyOrder.Total.String()).
		Str("base quoted sell order total", baseQuotedSellOrder.Total.String()).
		Str("quoted sell order total", quotedSellOrder.Total.String()).
		Msg("option 1 info")

	// option 1 // buy base for usdt / sell base for quoted / sell quoted for usdt
	if baseBuyOrder.Total.Cmp(baseUSDTPair.MinVolume) == 1 &&
		baseQuotedSellOrder.Total.Cmp(baseQuotedPair.MinVolume) == 1 &&
		quotedSellOrder.Total.Cmp(quotedUSDTPair.MinVolume) == 1 {
		baseBuyOrderAmount := big.NewFloat(0).Quo(baseUSDTPair.MinVolume, baseBuyOrder.Price)

		s.logger.Debug().
			Str("pair", baseUSDTPair.GetPairName()).
			Str("amount", baseUSDTPair.MinVolume.Text('f', 10)).
			Str("price", baseBuyOrder.Price.Text('f', baseUSDTPair.PriceScale)).
			Str("total", baseBuyOrderAmount.Text('f', 10)).
			Msg("buy base")

		baseQuotedSellOrderTotal := big.NewFloat(0).Mul(baseBuyOrderAmount, baseQuotedSellOrder.Price)

		s.logger.Debug().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("amount", baseUSDTPair.MinVolume.Text('f', 10)).
			Str("price", baseQuotedSellOrder.Price.Text('f', baseQuotedPair.PriceScale)).
			Str("total", baseQuotedSellOrderTotal.Text('f', 10)).
			Msg("sell base for quoted")

		quotedSellOrderUSDTAmount := big.NewFloat(0).Mul(baseQuotedSellOrderTotal, quotedSellOrder.Price)

		s.logger.Debug().
			Str("pair", quotedUSDTPair.GetPairName()).
			Str("amount", baseQuotedSellOrderTotal.Text('f', 10)).
			Str("price", quotedSellOrder.Price.Text('f', baseUSDTPair.PriceScale)).
			Str("total", quotedSellOrderUSDTAmount.Text('f', 10)).
			Msg("sell quoted")

		// 100 - (x * 100 / y)
		spread := big.NewFloat(0).Sub(
			dictionary.MaxPercentFloat,
			big.NewFloat(0).
				Quo(big.NewFloat(0).Mul(baseUSDTPair.MinVolume, dictionary.MaxPercentFloat), quotedSellOrderUSDTAmount),
		)

		s.logger.Info().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("before usdt amount", baseUSDTPair.MinVolume.Text('f', 10)).
			Str("after usdt amount", quotedSellOrderUSDTAmount.Text('f', 10)).
			Str("spread", spread.Text('f', 2)).
			Msg("pair spread")

		if spread.Cmp(big.NewFloat(spreadForAlert)) == 1 {
			now := time.Now()
			if s.sentAt == nil || time.Now().After(s.sentAt.Add(sendInterval)) {
				s.sentAt = &now

				s.tgSvc.Send(
					fmt.Sprintf(
						`env: %s,
pair %s,
arbitrage available,
before usdt amount %s,
after usdt amount %s,
spread %s`,
						s.cfg.Env,
						baseQuotedPair.GetPairName(),
						baseUSDTPair.MinVolume.Text('f', 10),
						quotedSellOrderUSDTAmount.Text('f', 10),
						spread.Text('f', 2),
					))
			}
		}
	}
}

func (s *Arbitrage) checkBuyQuotedOptions(
	baseUSDTPair *storage.Pair,
	quotedBuyOrder *storage.BookOrder,
	quotedUSDTPair *storage.Pair,
	baseSellOrder *storage.BookOrder,
	baseQuotedPair *storage.Pair,
	baseQuotedBuyOrder *storage.BookOrder,
) {
	s.logger.Debug().
		Str("pair", baseQuotedPair.GetPairName()).
		Str("quoted buy order total", quotedBuyOrder.Total.String()).
		Str("base quoted buy order total", baseQuotedBuyOrder.Total.String()).
		Str("base sell order total", baseSellOrder.Total.String()).
		Msg("option 2 info")

	// option 2 // buy quoted for usdt / buy base for quoted / sell base for usdt
	if quotedBuyOrder.Total.Cmp(quotedUSDTPair.MinVolume) == 1 &&
		baseQuotedBuyOrder.Total.Cmp(baseQuotedPair.MinVolume) == 0 &&
		baseSellOrder.Total.Cmp(baseUSDTPair.MinVolume) == 0 {
		quotedBuyOrderAmount := big.NewFloat(0).Quo(quotedUSDTPair.MinVolume, quotedBuyOrder.Price)

		s.logger.Debug().
			Str("pair", quotedUSDTPair.GetPairName()).
			Str("amount", quotedUSDTPair.MinVolume.Text('f', 10)).
			Str("price", quotedBuyOrder.Price.Text('f', quotedUSDTPair.PriceScale)).
			Str("total", quotedBuyOrderAmount.Text('f', 10)).
			Msg("buy quoted")

		baseQuotedBuyOrderTotal := big.NewFloat(0).Mul(quotedBuyOrderAmount, baseQuotedBuyOrder.Price)

		s.logger.Debug().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("amount", baseQuotedBuyOrderTotal.Text('f', 10)).
			Str("price", baseQuotedBuyOrder.Price.Text('f', baseQuotedPair.PriceScale)).
			Str("total", baseQuotedPair.MinVolume.Text('f', 10)).
			Msg("buy base for quoted")

		baseSellOrderUSDTAmount := big.NewFloat(0).Mul(baseQuotedBuyOrderTotal, baseSellOrder.Price)

		s.logger.Debug().
			Str("pair", baseUSDTPair.GetPairName()).
			Str("amount", baseQuotedBuyOrderTotal.Text('f', 10)).
			Str("price", baseSellOrder.Price.Text('f', baseUSDTPair.PriceScale)).
			Str("total", baseSellOrderUSDTAmount.Text('f', 10)).
			Msg("sell quoted")

		// 100 - (x * 100 / y)
		spread := big.NewFloat(0).Sub(
			dictionary.MaxPercentFloat,
			big.NewFloat(0).
				Quo(big.NewFloat(0).Mul(quotedUSDTPair.MinVolume, dictionary.MaxPercentFloat), baseSellOrderUSDTAmount),
		)

		s.logger.Info().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("before usdt amount", quotedUSDTPair.MinVolume.Text('f', 10)).
			Str("after usdt amount", baseSellOrderUSDTAmount.Text('f', 10)).
			Str("spread", spread.Text('f', 2)).
			Msg("pair spread")

		if spread.Cmp(big.NewFloat(spreadForAlert)) == 1 {
			now := time.Now()
			if s.sentAt == nil || time.Now().After(s.sentAt.Add(sendInterval)) {
				s.sentAt = &now

				s.tgSvc.Send(
					fmt.Sprintf(
						`env: %s,
arbitrage available,
pair %s,
before usdt amount %s,
after usdt amount %s,
spread %s`,
						s.cfg.Env,
						baseQuotedPair.GetPairName(),
						quotedUSDTPair.MinVolume.Text('f', 10),
						baseSellOrderUSDTAmount.Text('f', 10),
						spread.Text('f', 2),
					))
			}
		}
	}
}
