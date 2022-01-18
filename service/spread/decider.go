package spread

import (
	"context"
	"errors"
	"math/big"
	"time"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/storage"
	storageSpread "github.com/soulgarden/kickex-bot/storage/spread"
)

type Decider struct {
	pair              *storage.Pair
	orderBook         *storage.Book
	storage           *storage.Storage
	spreadOrderSvc    *Order
	tgSvc             *Tg
	spreadForStartBuy *big.Float
	forceCheckBroker  *broker.Broker
	logger            *zerolog.Logger
}

func NewDecider(
	cfg *conf.Bot,
	pair *storage.Pair,
	orderBook *storage.Book,
	storage *storage.Storage,
	spreadOrderSvc *Order,
	tgSvc *Tg,
	forceCheckBroker *broker.Broker,
	logger *zerolog.Logger,
) (*Decider, error) {
	spreadForStartBuy, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStartBuy)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", cfg.Spread.SpreadForStartBuy).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Decider{
		pair:              pair,
		orderBook:         orderBook,
		storage:           storage,
		spreadOrderSvc:    spreadOrderSvc,
		tgSvc:             tgSvc,
		spreadForStartBuy: spreadForStartBuy,
		forceCheckBroker:  forceCheckBroker,
		logger:            logger,
	}, nil
}

func (s *Decider) Start(ctx context.Context, sess *storageSpread.Session) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("order creation decider starting...")

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("order creation decider stopped")

	e := s.orderBook.OrderBookEventBroker.Subscribe("order creation decider")
	forceCheckCh := s.forceCheckBroker.Subscribe("force check order creation decider")

	defer s.orderBook.OrderBookEventBroker.Unsubscribe(e)
	defer s.forceCheckBroker.Unsubscribe(forceCheckCh)

	for {
		select {
		case <-forceCheckCh:
			if isDone, err := s.decide(sess); isDone && err != nil {
				return err
			}
		case <-e:
			if isDone, err := s.decide(sess); isDone && err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Decider) decide(sess *storageSpread.Session) (isDone bool, err error) {
	if sess.GetIsDone() {
		return true, nil
	}

	if isBuyAvailable := s.isBuyOrderCreationAvailable(sess, false); isBuyAvailable {
		if err := s.spreadOrderSvc.createBuyOrder(sess); err != nil {
			s.logger.Err(err).Str("id", sess.ID).Msg("create buy order")

			if errors.Is(err, dictionary.ErrInsufficientFunds) {
				return false, nil
			}

			return true, err
		}
	} else if isSellAvailable := s.isSellOrderCreationAvailable(sess, false); isSellAvailable {
		if err := s.spreadOrderSvc.createSellOrder(sess); err != nil {
			s.logger.Err(err).Str("id", sess.ID).Msg("create sell order")

			if errors.Is(err, dictionary.ErrInsufficientFunds) {
				return false, nil
			}

			return true, err
		}
	}

	return false, nil
}

func (s *Decider) isBuyOrderCreationAvailable(sess *storageSpread.Session, force bool) bool {
	if !sess.GetIsNeedToCreateBuyOrder() && !force {
		return false
	}

	if s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.orderBook.GetSpread().Cmp(s.spreadForStartBuy) >= 0 {
		buyVolume, _, _ := s.spreadOrderSvc.calculateBuyOrderVolume(sess)

		maxBidPrice := s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)
		maxBid := s.orderBook.GetBid(maxBidPrice)

		if maxBid == nil {
			return false
		}

		if maxBid.Amount.Cmp(buyVolume) >= 0 {
			return true
		}
	}

	return false
}

func (s *Decider) isSellOrderCreationAvailable(sess *storageSpread.Session, force bool) bool {
	if !sess.GetIsNeedToCreateSellOrder() && !force {
		return false
	}

	if s.spreadOrderSvc.calcBuySpread(sess.GetActiveBuyOrderID()).Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.spreadOrderSvc.calcBuySpread(sess.GetActiveBuyOrderID()).Cmp(s.spreadOrderSvc.spreadForStartSell) == 1 {
		sellVolume, _, _ := s.spreadOrderSvc.calculateSellOrderVolume(sess)

		minAskPrice := s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)
		minAsk := s.orderBook.GetAsk(minAskPrice)

		if minAsk == nil {
			return false
		}

		if minAsk.Amount.Cmp(sellVolume) >= 0 {
			return true
		}
	}

	if sess.GetPrevSellOrderID() != 0 && s.orderBook.SpreadActiveSessionID.Load() == sess.ID {
		o := s.storage.GetUserOrder(sess.GetPrevSellOrderID())

		if time.Now().After(o.CreatedTimestamp.Add(time.Hour)) && o.LimitPrice.Cmp(s.orderBook.GetMaxBidPrice()) == -1 {
			s.logger.Warn().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Str("order price", o.LimitPrice.Text('f', s.pair.PriceScale)).
				Str("max bid price", s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)).
				Msg("allow to create new session after 1h of inability to create an order")

			s.tgSvc.sendTGAsyncAllowedToCreateNewSession(o)

			s.orderBook.SpreadActiveSessionID.Store("")
		}
	}

	return false
}
