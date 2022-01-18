package spread

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
	storageSpread "github.com/soulgarden/kickex-bot/storage/spread"
)

const orderCreationDuration = time.Second * 10

// Watch for created order.
type Watch struct {
	cfg            *conf.Bot
	pair           *storage.Pair
	orderBook      *storage.Book
	storage        *storage.Storage
	accEventBroker *broker.Broker
	orderSvc       *service.Order
	spreadOrderSvc *Order

	decider *Decider

	priceStep             *big.Float
	spreadForStopBuyTrade *big.Float

	forceCheckBroker *broker.Broker

	logger *zerolog.Logger
}

func NewWatch(
	cfg *conf.Bot,
	pair *storage.Pair,
	orderBook *storage.Book,
	storage *storage.Storage,
	accEventBroker *broker.Broker,
	orderSvc *service.Order,
	spreadOrderSvc *Order,
	decider *Decider,
	priceStep *big.Float,
	forceCheckBroker *broker.Broker,
	logger *zerolog.Logger,
) (*Watch, error) {
	spreadForStopBuyTrade, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStopBuyTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", cfg.Spread.SpreadForStopBuyTrade).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Watch{
		cfg:                   cfg,
		pair:                  pair,
		orderBook:             orderBook,
		storage:               storage,
		accEventBroker:        accEventBroker,
		orderSvc:              orderSvc,
		spreadOrderSvc:        spreadOrderSvc,
		decider:               decider,
		priceStep:             priceStep,
		spreadForStopBuyTrade: spreadForStopBuyTrade,
		forceCheckBroker:      forceCheckBroker,
		logger:                logger,
	}, nil
}

func (s *Watch) Start(ctx context.Context, sess *storageSpread.Session, oid int64) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", oid).
		Msg("start watch order process")

	bookEventCh := s.orderBook.OrderBookEventBroker.Subscribe("orderbook watch order")
	accEventCh := s.accEventBroker.Subscribe("accounting watch order")
	forceCheckCh := s.forceCheckBroker.Subscribe("force check watch order")

	defer s.orderBook.OrderBookEventBroker.Unsubscribe(bookEventCh)
	defer s.accEventBroker.Unsubscribe(accEventCh)
	defer s.forceCheckBroker.Unsubscribe(forceCheckCh)

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", oid).
		Msg("stop watch order process")

	startedTime := time.Now()

	var hasFinalState bool

	var err error

	for {
		select {
		case _, ok := <-forceCheckCh:
			if hasFinalState, err = s.processEvent(ctx, sess, oid, ok, startedTime); hasFinalState || err != nil {
				return err
			}
		case _, ok := <-accEventCh:
			if hasFinalState, err = s.processEvent(ctx, sess, oid, ok, startedTime); hasFinalState || err != nil {
				return err
			}
		case _, ok := <-bookEventCh:
			if hasFinalState, err = s.processEvent(ctx, sess, oid, ok, startedTime); hasFinalState || err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Watch) processEvent(
	ctx context.Context,
	sess *storageSpread.Session,
	oid int64,
	ok bool,
	startedTime time.Time,
) (hasFinalState bool, err error) {
	if !ok {
		s.logger.Err(dictionary.ErrEventChannelClosed).Msg("book/accounting event channel closed")

		return true, dictionary.ErrEventChannelClosed
	}

	hasFinalState, err = s.checkOrderState(ctx, oid, sess, &startedTime)
	if err != nil {
		return hasFinalState, err
	}

	return hasFinalState, nil
}

func (s *Watch) checkOrderState(
	ctx context.Context,
	oid int64,
	sess *storageSpread.Session,
	startedTime *time.Time,
) (hasFinalState bool, err error) {
	order, err := s.getOrderFromStorage(ctx, oid, sess, startedTime)
	if err != nil {
		s.logger.Err(err).Int64("oid", oid).Msg("get order from storage")

		return false, err
	}

	if order == nil {
		return false, nil
	}

	if order.State < dictionary.StateActive {
		s.logger.Warn().Int64("oid", oid).Int("state", order.State).Msg("order state is below active")

		return false, nil
	}

	// stop manage order if executed
	if order.State > dictionary.StateActive {
		if order.TradeIntent == dictionary.BuyBase {
			return s.buyOrderExecuted(sess, order, oid)
		} else if order.TradeIntent == dictionary.SellBase {
			if err := s.sellOrderExecuted(sess, order, oid); err != nil {
				s.logger.Err(err).Int64("oid", oid).Msg("set order executed")

				return false, err
			}
		}

		return true, nil
	}

	if order.TradeIntent == dictionary.BuyBase {
		spread := s.spreadOrderSvc.calcBuySpread(sess.GetActiveBuyOrderID())

		if spread.Cmp(s.spreadForStopBuyTrade) == -1 && s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 {
			return s.cancelBuyOrder(sess, oid, spread)
		}

		if s.isMoveBuyOrderRequired(sess, order) {
			return s.moveBuyOrder(sess, order, oid)
		}
	}

	if order.TradeIntent == dictionary.SellBase {
		spread := s.spreadOrderSvc.calcBuySpread(sess.GetActiveBuyOrderID())

		// cancel sell order
		if spread.Cmp(s.spreadForStopBuyTrade) == -1 {
			return s.cancelSellOrder(sess, oid, spread)
		}

		if s.isMoveSellOrderRequired(sess, order) {
			return s.moveSellOrder(sess, oid)
		}
	}

	return false, nil
}

func (s *Watch) moveSellOrder(sess *storageSpread.Session, oid int64) (hasFinalState bool, err error) {
	isSellAvailable := s.decider.isSellOrderCreationAvailable(sess, true)

	if isSellAvailable {
		rid, extID, err := s.spreadOrderSvc.moveSellOrder(sess, oid)
		if err != nil {
			s.logger.Err(err).Msg("move sell order")

			return false, err
		}

		sess.SetPrevSellOrderID(oid)
		sess.SetActiveSellExtOrderID(extID)
		sess.SetActiveSellOrderRequestID(strconv.FormatInt(rid, dictionary.DefaultIntBase))

		return true, nil
	}

	err = s.orderSvc.CancelOrder(oid)
	if err != nil {
		if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
			s.logger.Warn().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", oid).
				Msg("expected cancelled sell order state, but got done")

			s.forceCheckBroker.Publish(struct{}{})

			return false, nil
		}

		s.logger.Err(err).Int64("oid", oid).Msg("can't cancel order")

		return false, err
	}

	sess.SetPrevSellOrderID(oid)
	sess.SetActiveSellOrderID(0)
	sess.SetIsNeedToCreateSellOrder(true)

	return true, nil
}

func (s *Watch) moveBuyOrder(sess *storageSpread.Session, order *storage.Order, oid int64) (
	hasFinishedState bool,
	err error,
) {
	isBuyAvailable := s.decider.isBuyOrderCreationAvailable(sess, true)

	if isBuyAvailable {
		rid, extID, err := s.spreadOrderSvc.moveBuyOrder(sess)
		if err != nil {
			s.logger.
				Err(err).
				Str("id", sess.ID).
				Int64("oid", order.ID).
				Msg("move buy order")

			return false, err
		}

		sess.SetPrevBuyOrderID(oid)
		sess.SetActiveBuyExtOrderID(extID)
		sess.SetActiveBuyOrderRequestID(strconv.FormatInt(rid, dictionary.DefaultIntBase))

		return true, nil
	}

	err = s.orderSvc.CancelOrder(oid)
	if err != nil {
		if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
			s.logger.Warn().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", oid).
				Msg("expected cancelled buy order state, but got done")

			s.forceCheckBroker.Publish(struct{}{})

			return false, nil
		}

		s.logger.Err(err).Int64("oid", oid).Msg("cancel buy order for move")

		return false, err
	}

	sess.SetPrevBuyOrderID(oid)
	sess.SetActiveBuyOrderID(0)
	sess.SetIsNeedToCreateBuyOrder(true)

	return true, nil
}

func (s *Watch) cancelBuyOrder(sess *storageSpread.Session, oid int64, spread *big.Float) (hasFinalState bool, err error) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", oid).
		Str("spread", spread.String()).
		Msg("time to cancel buy order")

	err = s.orderSvc.CancelOrder(oid)
	if err != nil {
		if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
			s.logger.Warn().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", oid).
				Msg("expected cancelled state, but got done")

			s.forceCheckBroker.Publish(struct{}{})

			return false, nil
		}

		s.logger.Err(err).
			Str("id", sess.ID).
			Str("pair", s.pair.GetPairName()).
			Int64("oid", oid).
			Msg("cancel buy order")

		return false, err
	}

	sess.SetPrevBuyOrderID(oid)
	sess.SetActiveBuyOrderID(0)
	sess.SetIsNeedToCreateBuyOrder(true)

	return true, nil
}

func (s *Watch) cancelSellOrder(sess *storageSpread.Session, oid int64, spread *big.Float) (
	hasFinishedState bool,
	err error,
) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", oid).
		Str("spread", spread.String()).
		Msg("time to cancel sell order")

	err = s.orderSvc.CancelOrder(oid)
	if err != nil {
		if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
			s.logger.Warn().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", oid).
				Msg("expected cancelled sell order state, but got done")

			s.forceCheckBroker.Publish(struct{}{})

			return false, nil
		}

		s.logger.Err(err).
			Str("id", sess.ID).
			Str("pair", s.pair.GetPairName()).
			Int64("oid", oid).
			Msg("can't cancel sell order")

		return false, err
	}

	sess.SetPrevSellOrderID(oid)
	sess.SetActiveSellOrderID(0)
	sess.SetIsNeedToCreateSellOrder(true)

	return true, nil
}

func (s *Watch) getOrderFromStorage(
	ctx context.Context,
	oid int64,
	sess *storageSpread.Session,
	startedTime *time.Time,
) (*storage.Order, error) {
	o := s.storage.GetUserOrder(oid)
	if o == nil {
		s.logger.Warn().Int64("oid", oid).Msg("order not found")

		if startedTime.Add(orderCreationDuration).Before(time.Now()) {
			s.logger.Err(dictionary.ErrOrderCreationEventNotReceived).Msg("order creation event not received")

			err := s.spreadOrderSvc.UpdateOrderStateByID(ctx, oid)
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", oid).
					Msg("update buy order state by id")

				return nil, err
			}
		}

		return o, nil
	}

	return o, nil
}

func (s *Watch) buyOrderExecuted(sess *storageSpread.Session, order *storage.Order, oid int64) (bool, error) {
	s.logger.Warn().
		Str("id", sess.ID).
		Int("state", order.State).
		Int64("oid", oid).
		Msg("buy order reached final state")

	if order.State == dictionary.StateDone {
		err := s.spreadOrderSvc.SetBuyOrderExecutedFlags(sess, order)

		s.logger.Err(err).Int64("oid", oid).Msg("set buy order executed flags")

		return true, err
	}

	if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
		sess.SetPrevBuyOrderID(oid)
		sess.SetActiveBuyOrderID(0)
		sess.SetIsNeedToCreateBuyOrder(true)

		s.forceCheckBroker.Publish(struct{}{})

		return true, nil
	}

	return false, nil
}

func (s *Watch) sellOrderExecuted(sess *storageSpread.Session, order *storage.Order, oid int64) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Int("state", order.State).
		Int64("oid", oid).
		Msg("sell order reached final state")

	if order.State == dictionary.StateDone {
		if err := s.spreadOrderSvc.SetSellOrderExecutedFlags(sess, order); err != nil {
			s.logger.Err(err).Int64("oid", order.ID).Msg("set sell order executed flags")

			return err
		}
	} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
		sess.SetActiveSellOrderID(0)
		sess.SetIsNeedToCreateSellOrder(true)

		s.forceCheckBroker.Publish(struct{}{})
	}

	return nil
}

func (s *Watch) isMoveBuyOrderRequired(sess *storageSpread.Session, o *storage.Order) bool {
	previousPossiblePrice := big.NewFloat(0).Sub(o.LimitPrice, s.priceStep)

	prevPriceExists := s.orderBook.GetBid(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
	nextPriceExists := s.orderBook.GetMaxBidPrice().Cmp(o.LimitPrice) == 1

	isReq := nextPriceExists || !prevPriceExists

	if !isReq {
		return false
	}

	spread := s.spreadOrderSvc.calcBuySpread(sess.GetActiveBuyOrderID())

	nextBidPrice := s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)

	nextBidAmountStr := ""

	nextBid := s.orderBook.GetBid(nextBidPrice)
	if nextBid != nil {
		nextBidAmountStr = nextBid.Amount.String()
	}

	s.logger.Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("oid", o.ID).
		Str("spread", spread.String()).
		Str("order price", o.LimitPrice.Text('f', s.pair.PriceScale)).
		Str("max bid price", nextBidPrice).
		Bool("next bid price exists", nextPriceExists).
		Str("next bid volume", nextBidAmountStr).
		Str("volume", o.OrderedVolume.String()).
		Str("prev possible bid price", previousPossiblePrice.Text('f', -1)).
		Bool("prev possible bid price exists", prevPriceExists).
		Msg("time to move buy order")

	return true
}

func (s *Watch) isMoveSellOrderRequired(sess *storageSpread.Session, o *storage.Order) bool {
	previousPossiblePrice := big.NewFloat(0).Add(o.LimitPrice, s.priceStep)

	prevPriceExists := s.orderBook.GetAsk(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
	nextPriceExists := s.orderBook.GetMinAskPrice().Cmp(o.LimitPrice) == -1

	isReq := nextPriceExists || !prevPriceExists

	if !isReq {
		return false
	}

	spread := s.spreadOrderSvc.calcBuySpread(sess.GetActiveBuyOrderID())

	s.logger.Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("oid", o.ID).
		Str("order price", o.LimitPrice.Text('f', s.pair.PriceScale)).
		Str("min ask price", s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)).
		Str("spread", spread.String()).
		Bool("next ask price exists", nextPriceExists).
		Str("prev possible ask price", previousPossiblePrice.Text('f', -1)).
		Bool("prev possible ask price exists", prevPriceExists).
		Msg("time to move sell order")

	return true
}
