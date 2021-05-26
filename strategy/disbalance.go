package strategy

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

const sendInterval = 30

type Disbalance struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *broker.Broker
	conversion  *service.Conversion
	tgSvc       *service.Telegram
	sentAt      time.Time
	logger      *zerolog.Logger
}

func NewDisbalance(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	conversion *service.Conversion,
	tgSvc *service.Telegram,
	logger *zerolog.Logger,
) *Disbalance {
	return &Disbalance{
		cfg:         cfg,
		storage:     storage,
		eventBroker: eventBroker,
		conversion:  conversion,
		tgSvc:       tgSvc,
		logger:      logger,
	}
}

func (s *Disbalance) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan os.Signal) {
	defer wg.Done()

	ch := make(chan int8, len(s.cfg.TrackingPairs))

	for _, pairName := range s.cfg.TrackingPairs {
		pair := s.storage.GetPair(pairName)

		e := s.storage.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].EventBroker.Subscribe()

		go func() {
			for range e {
				ch <- 0
			}
		}()

		defer s.storage.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].EventBroker.Unsubscribe(e)
	}

	s.logger.Warn().Msg("disbalance process started")
	defer s.logger.Warn().Msg("stop disbalance process")

	s.sentAt = time.Now()

	for {
		select {
		case <-ch:
			s.check()
		case <-ctx.Done():
			return
		}
	}
}

func (s *Disbalance) check() {
	for baseCurrency, quotedCurrencies := range s.storage.OrderBooks {
		var quotePrice *big.Float

		var err error

		minAskPrice := big.NewFloat(0)
		minAskPair := ""

		maxBidPrice := big.NewFloat(0)
		maxBidPair := ""

		for quotedCurrency, book := range quotedCurrencies {
			quotePrice, err = s.conversion.GetUSDTPrice(quotedCurrency)
			if err != nil {
				s.logger.Err(err).Str("currency", quotedCurrency).Msg("get usdt price")
			}

			pairName := baseCurrency + "/" + quotedCurrency
			pair := s.storage.GetPair(pairName)

			s.logger.Debug().
				Str("pair", pairName).
				Str("p", book.GetMinAskPrice().Text('f', pair.PriceScale)).
				Msg("min ask price")

			s.logger.Debug().
				Str("pair", baseCurrency+"/"+quotedCurrency).
				Str("p", book.GetMaxBidPrice().Text('f', pair.PriceScale)).
				Msg("max bid price")

			bookMinAskPrice := big.NewFloat(0).Mul(quotePrice, book.GetMinAskPrice())
			bookMaxBidPrice := big.NewFloat(0).Mul(quotePrice, book.GetMaxBidPrice())

			if minAskPrice.Cmp(dictionary.ZeroBigFloat) == 0 || bookMinAskPrice.Cmp(minAskPrice) == -1 {
				minAskPrice = bookMinAskPrice
				minAskPair = pairName
			}

			if maxBidPrice.Cmp(dictionary.ZeroBigFloat) == 0 || bookMaxBidPrice.Cmp(maxBidPrice) == 1 {
				maxBidPrice = bookMaxBidPrice
				maxBidPair = pairName
			}
		}

		s.logger.Info().
			Str("base", baseCurrency).
			Str("bid pair", maxBidPair).
			Str("max bid price", maxBidPrice.Text('f', 10)).
			Str("ask pair", minAskPair).
			Str("min ask price", minAskPrice.Text('f', 10)).
			Msg("lowest book prices in usd")

		if maxBidPrice.Cmp(minAskPrice) > 0 {
			s.logger.Warn().
				Str("base", baseCurrency).
				Str("bid pair", maxBidPair).
				Str("max bid price", maxBidPrice.Text('f', 10)).
				Str("ask pair", minAskPair).
				Str("min ask price", minAskPrice.Text('f', 10)).
				Msg("max bid price larger than min ask price")

			if time.Since(s.sentAt).Seconds() < sendInterval {
				s.sentAt = time.Now()

				s.tgSvc.Send(
					fmt.Sprintf(
						`env: %s, bid price larger than ask price, bid pair %s, ask pair %s`,
						s.cfg.Env,
						maxBidPair,
						maxBidPair,
					))
			}
		}
	}
}
