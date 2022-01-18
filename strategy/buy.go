package strategy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mailru/easyjson"

	buySvc "github.com/soulgarden/kickex-bot/service/buy"
	"github.com/soulgarden/kickex-bot/storage/buy"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

const buySessCreationInterval = 2 * time.Minute

type Buy struct {
	cfg            *conf.Bot
	pair           *storage.Pair
	orderBook      *storage.Book
	storage        *storage.Storage
	wsEventBroker  *broker.Broker
	accEventBroker *broker.Broker
	conversion     *service.Conversion
	tgSvc          *service.Telegram
	wsSvc          *service.WS
	orderSvc       *service.Order
	sessSvc        *buySvc.Session

	priceStep             *big.Float
	spreadForStartBuy     *big.Float
	spreadForStopBuyTrade *big.Float
	totalBuyInUSDT        *big.Float

	logger *zerolog.Logger
}

func NewBuy(
	cfg *conf.Bot,
	storage *storage.Storage,
	wsEventBroker *broker.Broker,
	accEventBroker *broker.Broker,
	conversion *service.Conversion,
	tgSvc *service.Telegram,
	wsSvc *service.WS,
	pair *storage.Pair,
	orderBook *storage.Book,
	orderSvc *service.Order,
	sessSvc *buySvc.Session,
	logger *zerolog.Logger,
) (*Buy, error) {
	zeroStep := big.NewFloat(0).Text('f', pair.PriceScale)
	priceStepStr := zeroStep[0:pair.PriceScale+1] + "1"

	priceStep, ok := big.NewFloat(0).SetPrec(uint(pair.PriceScale)).SetString(priceStepStr)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartTrade, ok := big.NewFloat(0).SetString(cfg.Buy.SpreadForStartBuy)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopBuyTrade, ok := big.NewFloat(0).SetString(cfg.Buy.SpreadForStopBuyTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	totalBuyInUSDT, ok := big.NewFloat(0).SetString(cfg.Buy.TotalBuyInUSDT)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", cfg.Buy.TotalBuyInUSDT).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Buy{
		cfg:                   cfg,
		pair:                  pair,
		storage:               storage,
		wsEventBroker:         wsEventBroker,
		accEventBroker:        accEventBroker,
		conversion:            conversion,
		tgSvc:                 tgSvc,
		wsSvc:                 wsSvc,
		priceStep:             priceStep,
		spreadForStartBuy:     spreadForStartTrade,
		spreadForStopBuyTrade: spreadForStopBuyTrade,
		totalBuyInUSDT:        totalBuyInUSDT,
		orderBook:             orderBook,
		orderSvc:              orderSvc,
		sessSvc:               sessSvc,
		logger:                logger,
	}, nil
}

func (s *Buy) Start(ctx context.Context, g *errgroup.Group) error {
	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("buy manager starting...")
	defer s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("buy manager stopped")

	ticker := time.NewTicker(buySessCreationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders starting")

			if err := s.cleanUpActiveOrders(); err != nil {
				s.logger.Err(err).Msg("cleanup active orders")

				return err
			}

			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders finished")

			return nil
		case <-ticker.C:
			if s.orderBook.BuyActiveSessionID.Load() != "" {
				continue
			}

			g.Go(func() error { return s.CreateSession(ctx, g) })
		}
	}
}

func (s *Buy) CreateSession(ctx context.Context, g *errgroup.Group) error {
	volume, err := s.getStartBuyVolume()
	if err != nil {
		s.logger.Err(err).Msg("get start buy volume")

		return err
	}

	sessCtx, cancel := context.WithCancel(ctx)

	defer cancel()

	sess := s.orderBook.NewBuySession(volume)

	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("start session")

	err = s.processSession(sessCtx, g, sess)
	s.logger.Err(err).
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("session finished")

	if s.orderBook.BuyActiveSessionID.Load() == sess.ID {
		s.orderBook.BuyActiveSessionID.Store("")
	}

	return nil
}

func (s *Buy) processSession(
	ctx context.Context,
	g *errgroup.Group,
	sess *buy.Session,
) error {
	g.Go(func() error {
		if err := s.listenNewOrders(ctx, g, sess); err != nil {
			s.logger.Err(err).Str("id", sess.ID).Msg("listen new orders")

			return err
		}

		return nil
	})

	return s.orderCreationDecider(ctx, sess)
}

func (s *Buy) orderCreationDecider(ctx context.Context, sess *buy.Session) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("order creation decider starting...")

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("order creation decider stopped")

	e := s.orderBook.OrderBookEventBroker.Subscribe("order creation decider")
	defer s.orderBook.OrderBookEventBroker.Unsubscribe(e)

	for {
		select {
		case <-e:
			if sess.GetIsDone() {
				return nil
			}

			isBuyAvailable := s.isBuyOrderCreationAvailable(sess, false)

			if isBuyAvailable {
				if err := s.createBuyOrder(sess); err != nil {
					s.logger.Err(err).Str("id", sess.ID).Msg("create buy order")

					if errors.Is(err, dictionary.ErrInsufficientFunds) {
						continue
					}

					return err
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Buy) isBuyOrderCreationAvailable(sess *buy.Session, force bool) bool {
	if !sess.GetIsNeedToCreateBuyOrder() && !force {
		return false
	}

	if s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.orderBook.GetSpread().Cmp(s.spreadForStartBuy) >= 0 {
		buyVolume, _, _ := s.calculateBuyOrderVolume(sess)

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

func (s *Buy) listenNewOrders(
	ctx context.Context,
	g *errgroup.Group,
	sess *buy.Session,
) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("listen new orders subscriber starting...")

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("listen new orders subscriber stopped")

	eventsCh := s.wsEventBroker.Subscribe("listen new orders")
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	var skip bool

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				return nil
			}

			msg, ok := e.([]byte)
			if !ok {
				return dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}
			err := easyjson.Unmarshal(msg, rid)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			if sess.GetActiveBuyOrderRequestID() != rid.ID {
				continue
			}

			s.logger.Warn().
				Bytes("payload", msg).
				Msg("got message")

			skip, err = s.checkListenOrderErrors(msg, sess)
			if err != nil {
				return err
			}

			if skip {
				continue
			}

			co := &response.CreatedOrder{}

			err = easyjson.Unmarshal(msg, co)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			if sess.GetActiveBuyOrderRequestID() == rid.ID {
				sess.SetActiveBuyOrderID(co.OrderID)
				sess.SetActiveBuyExtOrderID("")
				sess.SetActiveBuyOrderRequestID("")
				sess.AddBuyOrder(co.OrderID)
			}

			g.Go(func() error { return s.watchOrder(ctx, sess, co.OrderID) })

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Buy) checkListenOrderErrors(msg []byte, sess *buy.Session) (isSkipRequired bool, err error) {
	er := &response.Error{}

	err = easyjson.Unmarshal(msg, er)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return false, err
	}

	if er.Error != nil {
		// probably prev order executed on max available amount
		if er.Error.Code == response.AmountTooSmallCode && sess.GetPrevBuyOrderID() != 0 {
			if er.ID == sess.GetActiveBuyOrderRequestID() {
				s.logger.Warn().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", sess.GetPrevBuyOrderID()).
					Str("ext oid", sess.GetActiveBuyExtOrderID()).
					Msg("consider prev buy order as executed")

				if err = s.setBuyOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevBuyOrderID())); err != nil {
					s.logger.Err(err).Int64("oid", sess.GetPrevBuyOrderID()).Msg("set buy order executed flags")

					return false, err
				}
			}

			return true, nil
		} else if er.Error.Code == response.DoneOrderCode &&
			(sess.GetPrevBuyOrderID() != 0) {
			if er.ID == sess.GetActiveBuyOrderRequestID() {
				o := s.storage.GetUserOrder(sess.GetPrevBuyOrderID())
				if o.State == dictionary.StateCancelled {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevBuyOrderID()).
						Str("ext oid", sess.GetActiveBuyExtOrderID()).
						Msg("altered buy order already cancelled")

					sess.SetActiveBuyExtOrderID("")
					sess.SetActiveBuyOrderRequestID("")
					sess.SetIsNeedToCreateBuyOrder(true)

					return true, nil
				} else if o.State == dictionary.StateDone {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevBuyOrderID()).
						Str("ext oid", sess.GetActiveBuyExtOrderID()).
						Msg("altered buy order already done")

					if err = s.setBuyOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevBuyOrderID())); err != nil {
						s.logger.Err(err).Int64("oid", sess.GetPrevBuyOrderID()).Msg("set buy order executed flags")

						return false, err
					}

					return true, nil
				}
			}
		}

		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)

		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return false, err
	}

	return false, nil
}

func (s *Buy) createBuyOrder(sess *buy.Session) error {
	sess.SetIsNeedToCreateBuyOrder(false)

	var err error

	amount, total, price := s.calculateBuyOrderVolume(sess)

	s.logger.
		Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("prev order id", sess.GetPrevBuyOrderID()).
		Str("spread", s.orderBook.GetSpread().Text('f', s.pair.PriceScale)).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Msg("time to place buy order")

	if s.storage.GetBalance(s.pair.QuoteCurrency).Available.Cmp(total) == -1 {
		sess.SetIsNeedToCreateBuyOrder(true)

		return dictionary.ErrInsufficientFunds
	}

	rid, extID, err := s.wsSvc.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.BuyBase,
	)
	if err != nil {
		s.logger.Err(err).Msg("create buy order")

		return err
	}

	sess.SetActiveBuyExtOrderID(extID)
	sess.SetActiveBuyOrderRequestID(strconv.FormatInt(rid, dictionary.DefaultIntBase))

	return nil
}

func (s *Buy) watchOrder(ctx context.Context, sess *buy.Session, orderID int64) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("start watch order process")

	bookEventCh := s.orderBook.OrderBookEventBroker.Subscribe("watch order")

	defer s.orderBook.OrderBookEventBroker.Unsubscribe(bookEventCh)

	accEventCh := s.accEventBroker.Subscribe("watch order")

	defer s.accEventBroker.Unsubscribe(accEventCh)

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("stop watch order process")

	startedTime := time.Now()

	for {
		select {
		case _, ok := <-accEventCh:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return dictionary.ErrEventChannelClosed
			}

			hasFinalState, err := s.checkOrderState(ctx, orderID, sess, &startedTime)
			if err != nil {
				return err
			}

			if hasFinalState {
				return nil
			}
		case <-bookEventCh:
			hasFinalState, err := s.checkOrderState(ctx, orderID, sess, &startedTime)
			if err != nil {
				return err
			}

			if hasFinalState {
				return nil
			}

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Buy) checkOrderState(
	ctx context.Context,
	orderID int64,
	sess *buy.Session,
	startedTime *time.Time,
) (hasFinalState bool, err error) {
	// order sent, wait creation
	order := s.storage.GetUserOrder(orderID)
	if order == nil {
		s.logger.Warn().Int64("oid", orderID).Msg("order not found")

		if startedTime.Add(orderCreationDuration).Before(time.Now()) {
			s.logger.Err(dictionary.ErrOrderCreationEventNotReceived).Msg("order creation event not received")

			err := s.updateOrderStateByID(ctx, orderID)
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("update buy order state by id")

				return false, err
			}
		}

		return false, nil
	}

	if order.State < dictionary.StateActive {
		s.logger.Warn().
			Int64("oid", orderID).
			Int("state", order.State).
			Msg("order state is below active")

		return false, nil
	}

	// stop manage order if executed
	if order.State > dictionary.StateActive {
		s.logger.Warn().
			Str("id", sess.ID).
			Int64("oid", orderID).
			Msg("buy order reached final state")

		if order.State == dictionary.StateDone {
			if err = s.setBuyOrderExecutedFlags(sess, order); err != nil {
				s.logger.Err(err).Int64("oid", order.ID).Msg("set buy order executed flags")

				return false, err
			}

			return true, nil
		}

		if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
			sess.SetPrevBuyOrderID(orderID)
			sess.SetActiveBuyOrderID(0)
			sess.SetIsNeedToCreateBuyOrder(true)

			return true, nil
		}

		return true, nil
	}

	spread := s.calcBuySpread(sess.GetActiveBuyOrderID())
	// cancel buy order
	if spread.Cmp(s.spreadForStopBuyTrade) == -1 && s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 {
		s.logger.Warn().
			Str("id", sess.ID).
			Str("pair", s.pair.GetPairName()).
			Int64("oid", orderID).
			Str("spread", spread.String()).
			Msg("time to cancel buy order")

		err := s.orderSvc.CancelOrder(orderID)
		if err != nil {
			if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
				s.logger.Warn().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("oid", orderID).
					Msg("expected cancelled state, but got done")

				s.orderBook.OrderBookEventBroker.Publish(struct{}{})

				return false, nil
			}

			s.logger.Err(err).
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", orderID).
				Msg("cancel buy order")

			return false, err
		}

		sess.SetPrevBuyOrderID(orderID)
		sess.SetActiveBuyOrderID(0)
		sess.SetIsNeedToCreateBuyOrder(true)

		return true, nil
	}

	if s.isMoveBuyOrderRequired(sess, order) {
		isBuyAvailable := s.isBuyOrderCreationAvailable(sess, true)

		if isBuyAvailable {
			rid, extID, err := s.moveBuyOrder(sess)
			if err != nil {
				s.logger.
					Err(err).
					Str("id", sess.ID).
					Int64("oid", order.ID).
					Msg("move buy order")

				return false, err
			}

			sess.SetPrevBuyOrderID(orderID)
			sess.SetActiveBuyExtOrderID(extID)
			sess.SetActiveBuyOrderRequestID(strconv.FormatInt(rid, dictionary.DefaultIntBase))

			s.orderBook.OrderBookEventBroker.Publish(struct{}{}) // don't wait change order book
		} else {
			err := s.orderSvc.CancelOrder(orderID)
			if err != nil {
				if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Msg("expected cancelled buy order state, but got done")

					s.orderBook.OrderBookEventBroker.Publish(struct{}{})

					return false, nil
				}

				s.logger.Err(err).Int64("oid", orderID).Msg("cancel buy order for move")

				return false, err
			}

			sess.SetPrevBuyOrderID(orderID)
			sess.SetActiveBuyOrderID(0)
			sess.SetIsNeedToCreateBuyOrder(true)

			s.orderBook.OrderBookEventBroker.Publish(struct{}{}) // don't wait change order book
		}

		return true, nil
	}

	return false, nil
}

func (s *Buy) calcBuySpread(activeBuyOrderID int64) *big.Float {
	if o := s.storage.GetUserOrder(activeBuyOrderID); o == nil {
		return dictionary.ZeroBigFloat
	}

	// 100 - (x * 100 / y)
	return big.NewFloat(0).Sub(
		dictionary.MaxPercentFloat,
		big.NewFloat(0).Quo(
			big.NewFloat(0).Mul(s.storage.GetUserOrder(activeBuyOrderID).LimitPrice, dictionary.MaxPercentFloat),
			s.orderBook.GetMinAskPrice()),
	)
}

func (s *Buy) moveBuyOrder(sess *buy.Session) (int64, string, error) {
	amount, total, price := s.calculateBuyOrderVolume(sess)

	id, extID, err := s.wsSvc.AlterOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.BuyBase,
		sess.ActiveBuyOrderID,
	)
	if err != nil {
		s.logger.Err(err).
			Str("pair", s.pair.GetPairName()).
			Str("ext oid", extID).
			Int64("prev order id", sess.ActiveBuyOrderID).
			Str("spread", s.orderBook.GetSpread().Text('f', s.pair.PriceScale)).
			Str("price", price.Text('f', s.pair.PriceScale)).
			Str("amount", amount.Text('f', s.pair.QuantityScale)).
			Str("total", total.String()).
			Msg("alter buy order")

		return 0, "", err
	}

	s.logger.
		Warn().
		Str("pair", s.pair.GetPairName()).
		Str("ext oid", extID).
		Int64("prev order id", sess.ActiveBuyOrderID).
		Str("spread", s.orderBook.GetSpread().Text('f', s.pair.PriceScale)).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Msg("alter buy order")

	return id, extID, nil
}

func (s *Buy) setBuyOrderExecutedFlags(sess *buy.Session, order *storage.Order) error {
	if order.State == dictionary.StateActive {
		err := s.orderSvc.CancelOrder(order.ID)
		if err != nil {
			if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
				s.logger.Warn().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("oid", order.ID).
					Msg("expected cancelled state, but got done")
			}

			s.logger.
				Err(err).
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", order.ID).
				Msg("cancel buy order")

			return err
		}
	}

	sess.SetBuyOrderExecutedFlags(order.ID)
	s.orderBook.AddBoughtCost(s.sessSvc.GetSessTotalBoughtCost(sess))
	s.orderBook.AddBoughtVolume(s.sessSvc.GetSessTotalBoughtVolume(sess))

	s.orderBook.OrderBookEventBroker.Publish(struct{}{})

	s.sendTGBuyOrderReachedDoneState(sess, order)

	return nil
}

func (s *Buy) cleanUpActiveOrders() error {
	for _, sess := range s.orderBook.SpreadSessions {
		if sess.GetIsDone() {
			continue
		}

		if sess.GetActiveBuyOrderID() > 1 {
			if o := s.storage.GetUserOrder(sess.GetActiveBuyOrderID()); o != nil &&
				o.State < dictionary.StateDone {
				if _, err := s.wsSvc.CancelOrder(sess.GetActiveBuyOrderID()); err != nil {
					s.logger.Err(err).
						Int64("oid", sess.GetActiveBuyOrderID()).
						Msg("send cancel buy order request")

					return err
				}

				s.logger.Warn().
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("send cancel buy order request")
			}
		}
	}

	return nil
}

func (s *Buy) getStartBuyVolume() (*big.Float, error) {
	quotedToUSDTPrices, err := s.conversion.GetUSDTPrice(s.pair.QuoteCurrency)
	if err != nil {
		return nil, err
	}

	totalBuyVolumeInQuoted := big.NewFloat(0).Quo(s.totalBuyInUSDT, quotedToUSDTPrices)

	if totalBuyVolumeInQuoted.Cmp(s.pair.MinVolume) == -1 {
		return s.pair.MinVolume, err
	}

	return totalBuyVolumeInQuoted, nil
}

func (s *Buy) calculateBuyOrderVolume(sess *buy.Session) (amount, total, price *big.Float) {
	price = big.NewFloat(0).Add(s.orderBook.GetMaxBidPrice(), s.priceStep)
	total = big.NewFloat(0).Sub(sess.GetBuyTotal(), s.sessSvc.GetSessTotalBoughtCost(sess))
	amount = big.NewFloat(0)

	amount.Quo(total, price)

	return amount, total, price
}

func (s *Buy) isMoveBuyOrderRequired(sess *buy.Session, o *storage.Order) bool {
	previousPossiblePrice := big.NewFloat(0).Sub(o.LimitPrice, s.priceStep)

	prevPriceExists := s.orderBook.GetBid(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
	nextPriceExists := s.orderBook.GetMaxBidPrice().Cmp(o.LimitPrice) == 1

	isReq := nextPriceExists || !prevPriceExists

	if !isReq {
		return false
	}

	spread := s.calcBuySpread(sess.GetActiveBuyOrderID())

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

func (s *Buy) updateOrderStateByID(ctx context.Context, oid int64) error {
	rid, err := s.wsSvc.GetOrder(oid)
	if err != nil {
		s.logger.Err(err).Int64("oid", oid).Msg("send get order by id request")

		return err
	}

	o, err := s.orderSvc.UpdateOrderState(ctx, rid)
	if err != nil || o == nil {
		s.logger.Err(err).
			Int64("oid", oid).
			Msg("get order by id failed")

		return err
	}

	return nil
}

func (s *Buy) sendTGBuyOrderReachedDoneState(sess *buy.Session, order *storage.Order) {
	s.tgSvc.SendAsync(fmt.Sprintf(
		`env: %s,
buy order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s,
cost: %s,
total bought cost: %s,
total bought volume %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		s.sessSvc.GetSessTotalBoughtVolume(sess).Text('f', s.pair.QuantityScale),
		s.sessSvc.GetSessTotalBoughtCost(sess).Text('f', s.pair.PriceScale),
		s.orderBook.GetBoughtCost().Text('f', s.pair.PriceScale),
		s.orderBook.GetBoughtVolume().Text('f', s.pair.QuantityScale),
	))
}
