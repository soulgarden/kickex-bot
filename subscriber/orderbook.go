package subscriber

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"syscall"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type OrderBook struct {
	cfg         *conf.Bot
	pair        *response.Pair
	storage     *storage.Storage
	eventBroker *Broker
	orderBook   *storage.Book
	logger      *zerolog.Logger
}

func NewOrderBook(
	cfg *conf.Bot,
	st *storage.Storage,
	eventBroker *Broker,
	pair *response.Pair,
	logger *zerolog.Logger,
) *OrderBook {
	return &OrderBook{
		cfg:         cfg,
		storage:     st,
		eventBroker: eventBroker,
		pair:        pair,
		orderBook:   st.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency],
		logger:      logger,
	}
}

func (s *OrderBook) Start(ctx context.Context, interrupt chan os.Signal) {
	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("order book subscriber starting...")

	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")
		interrupt <- syscall.SIGSTOP

		return
	}

	defer cli.Close()

	err = cli.GetOrderBookAndSubscribe(s.pair.BaseCurrency + "/" + s.pair.QuoteCurrency)
	if err != nil {
		s.logger.Err(err).Msg("get order book and subscribe")
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case msg, ok := <-cli.ReadCh:
			if !ok {
				s.logger.Err(err).Msg("read channel closed")
				interrupt <- syscall.SIGSTOP

				return
			}

			r := &response.BookResponse{}

			s.logger.Debug().Bytes("payload", msg).Msg("got message")

			err := s.checkErrorResponse(msg)
			if err != nil {
				interrupt <- syscall.SIGSTOP

				return
			}

			err = json.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return
			}

			s.orderBook.LastPrice = r.LastPrice.Price

			err = s.updateMaxBidPrice(r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("update max bid price")

				interrupt <- syscall.SIGSTOP

				return
			}

			err = s.updateMinAskPrice(r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("update min ask price")

				interrupt <- syscall.SIGSTOP

				return
			}

			s.updateSpread()

			s.eventBroker.Publish(0)
		case <-ctx.Done():
			return
		}
	}
}

func (s *OrderBook) updateMaxBidPrice(r *response.BookResponse) error {
	if len(r.Bids) > 0 {
		for _, bid := range r.Bids {
			price, ok := big.NewFloat(0).SetString(bid.Price)
			if !ok {
				s.logger.Err(dictionary.ErrParseFloat).Msg("parse bid price")

				return dictionary.ErrParseFloat
			}

			if bid.Total == "" {
				s.orderBook.DeleteBid(price.Text('f', s.pair.PriceScale))

				continue
			}

			s.orderBook.AddBid(price.Text('f', s.pair.PriceScale), bid)
		}

		ok := s.orderBook.UpdateMaxBidPrice()
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).Msg("update max bid price error")

			return dictionary.ErrParseFloat
		}
	}

	return nil
}

func (s *OrderBook) updateMinAskPrice(r *response.BookResponse) error {
	if len(r.Asks) > 0 {
		for _, ask := range r.Asks {
			price, ok := big.NewFloat(0).SetString(ask.Price)
			if !ok {
				s.logger.Fatal().Err(dictionary.ErrParseFloat).Msg("parse ask price")

				return dictionary.ErrParseFloat
			}

			if ask.Total == "" {
				s.orderBook.DeleteAsk(price.Text('f', s.pair.PriceScale))

				continue
			}

			s.orderBook.AddAsk(price.Text('f', s.pair.PriceScale), ask)
		}

		ok := s.orderBook.UpdateMinAskPrice()
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).Msg("update min ask price error")

			return dictionary.ErrParseFloat
		}
	}

	return nil
}

func (s *OrderBook) updateSpread() {
	if s.orderBook.GetMaxBidPrice().Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.orderBook.GetMinAskPrice().Cmp(dictionary.ZeroBigFloat) == 1 {
		big.NewFloat(0).Mul(s.orderBook.GetMaxBidPrice(), dictionary.MaxPercentFloat)

		// 100 - (x * 100 / y)
		newSpread := big.NewFloat(0).Sub(
			dictionary.MaxPercentFloat,
			big.NewFloat(0).Quo(
				big.NewFloat(0).Mul(s.orderBook.GetMaxBidPrice(), dictionary.MaxPercentFloat),
				s.orderBook.GetMinAskPrice()),
		)

		if newSpread.Cmp(s.orderBook.GetSpread()) != 0 {
			s.logger.Debug().
				Str("old", s.orderBook.GetSpread().String()).
				Str("new", newSpread.String()).
				Str("bid", s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)).
				Str("ask", s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)).
				Msg("spread")

			s.orderBook.SetSpread(newSpread)
		}
	} else {
		s.orderBook.SetSpread(dictionary.ZeroBigFloat)
	}
}

func (s *OrderBook) checkErrorResponse(msg []byte) error {
	er := &response.Error{}

	err := json.Unmarshal(msg, er)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return err
	}

	if er.Error != nil {
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return errors.New(er.Error.Reason)
	}

	return nil
}
