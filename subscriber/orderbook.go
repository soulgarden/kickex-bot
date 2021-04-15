package subscriber

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

func SubscribeOrderBook(ctx context.Context, cfg *conf.Bot, storage *storage.Storage, eventCh chan int, logger *zerolog.Logger) error {
	pair, ok := cfg.Pairs[cfg.Pair]
	if !ok {
		logger.Fatal().Msg("pair is missing in pairs list")
	}

	cli, err := client.NewWsCli(cfg, logger)
	if err != nil {
		logger.Err(err).Msg("connection error")

		return err
	}

	defer cli.Close()

	err = cli.GetOrderBookAndSubscribe(cfg.Pair)
	if err != nil {
		logger.Err(err).Msg("get order book and subscribe")

		return err
	}

	for {
		select {
		case msg := <-cli.ReadCh:
			r := &response.BookResponse{}

			logger.Debug().Bytes("payload", msg).Msg("Got message")

			er := &response.Error{}

			err = json.Unmarshal(msg, er)
			if err != nil {
				logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")
			}

			if er.Error != nil {
				logger.Fatal().Bytes("response", msg).Err(err).Msg("received error")
			}

			err := json.Unmarshal(msg, r)
			if err != nil {
				logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			storage.Book.LastPrice = r.LastPrice.Price

			for _, bid := range r.Bids {
				price, ok := big.NewFloat(0).SetString(bid.Price)
				if !ok {
					logger.Fatal().Err(err).Msg("parse bid price")
				}

				if bid.Total == "" {
					ok = storage.Book.DeleteBid(price.Text('f', pair.PricePrecision))
					if !ok {
						logger.Fatal().Err(err).Msg("delete bid")
					}

					continue
				}

				ok = storage.Book.AddBid(price.Text('f', pair.PricePrecision), bid)
				if !ok {
					logger.Fatal().Err(err).Msg("add bid")
				}
			}

			for _, ask := range r.Asks {
				price, ok := big.NewFloat(0).SetString(ask.Price)
				if !ok {
					logger.Fatal().Err(err).Msg("parse ask price")
				}

				if ask.Total == "" {
					ok = storage.Book.DeleteAsk(price.Text('f', pair.PricePrecision))
					if !ok {
						logger.Fatal().Err(err).Msg("delete ask")
					}

					continue
				}

				ok = storage.Book.AddAsk(price.Text('f', pair.PricePrecision), ask)
				if !ok {
					logger.Fatal().Err(err).Msg("delete bid")
				}
			}

			if storage.Book.GetMaxBidPrice().Cmp(dictionary.ZeroBigFloat) == 1 &&
				storage.Book.GetMinAskPrice().Cmp(dictionary.ZeroBigFloat) == 1 {
				big.NewFloat(0).Mul(storage.Book.GetMaxBidPrice(), dictionary.MaxPercentFloat)

				// 100 - (x * 100 / y)
				newSpread := big.NewFloat(0).Sub(
					dictionary.MaxPercentFloat,
					big.NewFloat(0).Quo(
						big.NewFloat(0).Mul(storage.Book.GetMaxBidPrice(), dictionary.MaxPercentFloat),
						storage.Book.GetMinAskPrice()),
				)

				if newSpread.Cmp(storage.Book.Spread) != 0 {
					logger.Debug().
						Str("old", storage.Book.Spread.String()).
						Str("new", newSpread.String()).
						Str("bid", storage.Book.GetMaxBidPrice().Text('f', pair.PricePrecision)).
						Str("ask", storage.Book.GetMinAskPrice().Text('f', pair.PricePrecision)).
						Msg("spread")

					storage.Book.Spread = newSpread
				}
			} else {
				storage.Book.Spread = dictionary.ZeroBigFloat
			}

			eventCh <- 0
		case <-ctx.Done():
			cli.Close()

			return nil
		}
	}
}
