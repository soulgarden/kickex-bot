package subscriber

import (
	"context"
	"math/big"
	"os"
	"sync"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

type Disbalance struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *Broker
	conversion  *service.Conversion
	logger      *zerolog.Logger
}

func NewDisbalance(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *Broker,
	conversion *service.Conversion,
	logger *zerolog.Logger,
) *Disbalance {
	return &Disbalance{cfg: cfg, storage: storage, eventBroker: eventBroker, conversion: conversion, logger: logger}
}

func (s *Disbalance) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan os.Signal) {
	defer func() {
		wg.Done()
	}()

	e := s.eventBroker.Subscribe()

	defer func() {
		s.logger.
			Warn().
			Msg("stop disbalance process")

		s.eventBroker.Unsubscribe(e)
	}()

	for {
		select {
		case <-e:

		case <-e:
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

					if bookMinAskPrice.Cmp(minAskPrice) == 1 {
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
					Str("bid price", maxBidPrice.Text('f', 10)).
					Str("ask pair", minAskPair).
					Str("ask price", minAskPrice.Text('f', 10)).
					Msg("lowest book prices in usd")

				if maxBidPrice.Cmp(minAskPrice) == 1 {
					s.logger.Warn().
						Str("base", baseCurrency).
						Str("bid pair", maxBidPair).
						Str("bid price", maxBidPrice.Text('f', 10)).
						Str("ask pair", minAskPair).
						Str("ask price", minAskPrice.Text('f', 10)).
						Msg("bid price larger than ask price")
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
