package subscriber

import (
	"context"
	"encoding/json"
	"math/big"
	"os"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type OrderBook struct {
	cfg         *conf.Bot
	pair        *conf.Pair
	storage     *storage.Storage
	eventBroker *Broker
	logger      *zerolog.Logger
}

func NewOrderBook(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *Broker,
	logger *zerolog.Logger,
) (*OrderBook, error) {
	pair, ok := cfg.Pairs[cfg.Pair]
	if !ok {
		logger.Err(dictionary.ErrInvalidPair).Msg(dictionary.ErrInvalidPair.Error())

		return nil, dictionary.ErrInvalidPair
	}

	return &OrderBook{cfg: cfg, pair: pair, storage: storage, eventBroker: eventBroker, logger: logger}, nil
}

func (s *OrderBook) Start(ctx context.Context, interrupt chan os.Signal) error {
	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")

		return err
	}

	defer cli.Close()

	err = cli.GetOrderBookAndSubscribe(s.cfg.Pair)
	if err != nil {
		s.logger.Err(err).Msg("get order book and subscribe")

		return err
	}

	for {
		select {
		case msg := <-cli.ReadCh:
			r := &response.BookResponse{}

			s.logger.Debug().Bytes("payload", msg).Msg("got message")

			er := &response.Error{}

			err = json.Unmarshal(msg, er)
			if err != nil {
				s.logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")
			}

			if er.Error != nil {
				s.logger.Fatal().Bytes("response", msg).Err(err).Msg("received error")
			}

			err := json.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			s.storage.Book.LastPrice = r.LastPrice.Price

			for _, bid := range r.Bids {
				price, ok := big.NewFloat(0).SetString(bid.Price)
				if !ok {
					s.logger.Fatal().Err(err).Msg("parse bid price")
				}

				if bid.Total == "" {
					ok = s.storage.Book.DeleteBid(price.Text('f', s.pair.PricePrecision))
					if !ok {
						s.logger.Fatal().Err(err).Msg("delete bid")
					}

					continue
				}

				ok = s.storage.Book.AddBid(price.Text('f', s.pair.PricePrecision), bid)
				if !ok {
					s.logger.Fatal().Err(err).Msg("add bid")
				}
			}

			for _, ask := range r.Asks {
				price, ok := big.NewFloat(0).SetString(ask.Price)
				if !ok {
					s.logger.Fatal().Err(err).Msg("parse ask price")
				}

				if ask.Total == "" {
					ok = s.storage.Book.DeleteAsk(price.Text('f', s.pair.PricePrecision))
					if !ok {
						s.logger.Fatal().Err(err).Msg("delete ask")
					}

					continue
				}

				ok = s.storage.Book.AddAsk(price.Text('f', s.pair.PricePrecision), ask)
				if !ok {
					s.logger.Fatal().Err(err).Msg("delete bid")
				}
			}

			if s.storage.Book.GetMaxBidPrice().Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.storage.Book.GetMinAskPrice().Cmp(dictionary.ZeroBigFloat) == 1 {
				big.NewFloat(0).Mul(s.storage.Book.GetMaxBidPrice(), dictionary.MaxPercentFloat)

				// 100 - (x * 100 / y)
				newSpread := big.NewFloat(0).Sub(
					dictionary.MaxPercentFloat,
					big.NewFloat(0).Quo(
						big.NewFloat(0).Mul(s.storage.Book.GetMaxBidPrice(), dictionary.MaxPercentFloat),
						s.storage.Book.GetMinAskPrice()),
				)

				if newSpread.Cmp(s.storage.Book.Spread) != 0 {
					s.logger.Debug().
						Str("old", s.storage.Book.Spread.String()).
						Str("new", newSpread.String()).
						Str("bid", s.storage.Book.GetMaxBidPrice().Text('f', s.pair.PricePrecision)).
						Str("ask", s.storage.Book.GetMinAskPrice().Text('f', s.pair.PricePrecision)).
						Msg("spread")

					s.storage.Book.Spread = newSpread
				}
			} else {
				s.storage.Book.Spread = dictionary.ZeroBigFloat
			}

			s.eventBroker.Publish(0)
		case <-ctx.Done():
			cli.Close()

			return nil
		}
	}
}
