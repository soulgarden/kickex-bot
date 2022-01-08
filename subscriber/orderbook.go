package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/service"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type OrderBook struct {
	cfg         *conf.Bot
	pair        *storage.Pair
	storage     *storage.Storage
	eventBroker *broker.Broker
	wsSvc       *service.WS
	orderBook   *storage.Book
	logger      *zerolog.Logger
}

func NewOrderBook(
	cfg *conf.Bot,
	st *storage.Storage,
	eventBroker *broker.Broker,
	wsSvc *service.WS,
	pair *storage.Pair,
	orderBook *storage.Book,
	logger *zerolog.Logger,
) *OrderBook {
	return &OrderBook{
		cfg:         cfg,
		storage:     st,
		eventBroker: eventBroker,
		wsSvc:       wsSvc,
		pair:        pair,
		orderBook:   orderBook,
		logger:      logger,
	}
}

func (s *OrderBook) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan<- os.Signal) {
	defer func() {
		wg.Done()
	}()

	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("order book subscriber starting...")
	defer s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("order book subscriber stopped")

	eventsCh := s.eventBroker.Subscribe("order book subscriber")
	defer s.eventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.GetOrderBookAndSubscribe(s.pair.BaseCurrency + "/" + s.pair.QuoteCurrency)
	if err != nil {
		s.logger.Err(err).Msg("get order book and subscribe")

		interrupt <- syscall.SIGINT

		return
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGINT

				return
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGINT

				return
			}

			rid := &response.ID{}

			err := json.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return
			}

			if strconv.FormatInt(id, dictionary.DefaultIntBase) != rid.ID {
				continue
			}

			r := &response.BookResponse{}

			s.logger.Debug().Bytes("payload", msg).Msg("got message")

			err = s.checkErrorResponse(msg)
			if err != nil {
				s.logger.Err(err).Msg("check error response")

				interrupt <- syscall.SIGINT

				return
			}

			err = json.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return
			}

			s.orderBook.LastPrice = r.LastPrice.Price

			err = s.updateMaxBids(r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("update max bid price")

				interrupt <- syscall.SIGINT

				return
			}

			err = s.updateAsks(r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("update min ask price")

				interrupt <- syscall.SIGINT

				return
			}

			s.updateSpread()

			s.orderBook.OrderBookEventBroker.Publish(0)
		case <-ctx.Done():
			return
		}
	}
}

func (s *OrderBook) updateMaxBids(r *response.BookResponse) error {
	if len(r.Bids) > 0 {
		for _, bid := range r.Bids {
			price, ok := big.NewFloat(0).SetString(bid.Price)
			if !ok {
				s.logger.Err(dictionary.ErrParseFloat).Msg("parse bid price")

				return dictionary.ErrParseFloat
			}

			priceStr := price.Text('f', s.pair.PriceScale)

			if bid.Total == "" {
				s.orderBook.DeleteBid(priceStr)

				continue
			}

			o, err := s.createBookOrderByResponseOrder(bid, price)
			if err != nil {
				return err
			}

			s.orderBook.AddBid(priceStr, o)
		}

		ok := s.orderBook.UpdateMaxBidPrice()
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).Msg("update max bid price error")

			return dictionary.ErrParseFloat
		}
	}

	return nil
}

func (s *OrderBook) updateAsks(r *response.BookResponse) error {
	if len(r.Asks) > 0 {
		for _, ask := range r.Asks {
			price, ok := big.NewFloat(0).SetString(ask.Price)
			if !ok {
				s.logger.
					Err(dictionary.ErrParseFloat).
					Str("val", ask.Price).
					Msg("parse string as float")

				return dictionary.ErrParseFloat
			}

			priceStr := price.Text('f', s.pair.PriceScale)

			if ask.Total == "" {
				s.orderBook.DeleteAsk(priceStr)

				continue
			}

			o, err := s.createBookOrderByResponseOrder(ask, price)
			if err != nil {
				return err
			}

			s.orderBook.AddAsk(priceStr, o)
		}

		ok := s.orderBook.UpdateMinAskPrice()
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).Msg("update min ask price error")

			return dictionary.ErrParseFloat
		}
	}

	return nil
}

func (s *OrderBook) createBookOrderByResponseOrder(o *response.Order, price *big.Float) (*storage.BookOrder, error) {
	amount, ok := big.NewFloat(0).SetString(o.Amount)
	if !ok {
		s.logger.
			Err(dictionary.ErrParseFloat).
			Str("val", o.Amount).
			Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	total, ok := big.NewFloat(0).SetString(o.Total)
	if !ok {
		s.logger.
			Err(dictionary.ErrParseFloat).
			Str("val", o.Amount).
			Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &storage.BookOrder{
		Price:  price,
		Amount: amount,
		Total:  total,
	}, nil
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
		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return err
	}

	return nil
}
