package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

const sessCreationInterval = 10 * time.Millisecond

type Spread struct {
	cfg           *conf.Bot
	pair          *response.Pair
	orderBook     *storage.Book
	storage       *storage.Storage
	wsEventBroker *broker.Broker
	conversion    *service.Conversion
	tgSvc         *service.Telegram
	wsSvc         *service.WS
	orderSvc      *service.Order

	priceStep              *big.Float
	spreadForStartBuy      *big.Float
	spreadForStartSell     *big.Float
	spreadForStopBuyTrade  *big.Float
	spreadForStopSellTrade *big.Float

	logger *zerolog.Logger
}

func NewSpread(
	cfg *conf.Bot,
	storage *storage.Storage,
	wsEventBroker *broker.Broker,
	conversion *service.Conversion,
	tgSvc *service.Telegram,
	wsSvc *service.WS,
	pair *response.Pair,
	orderBook *storage.Book,
	orderSvc *service.Order,
	logger *zerolog.Logger,
) (*Spread, error) {
	zeroStep := big.NewFloat(0).Text('f', pair.PriceScale)
	priceStepStr := zeroStep[0:pair.PriceScale+1] + "1"

	priceStep, ok := big.NewFloat(0).SetPrec(uint(pair.PriceScale)).SetString(priceStepStr)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartTrade, ok := big.NewFloat(0).SetString(cfg.SpreadForStartBuy)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartSell, ok := big.NewFloat(0).SetString(cfg.SpreadForStartSell)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopBuyTrade, ok := big.NewFloat(0).SetString(cfg.SpreadForStopBuyTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopSellTrade, ok := big.NewFloat(0).SetString(cfg.SpreadForStopSellTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Spread{
		cfg:                    cfg,
		pair:                   pair,
		storage:                storage,
		wsEventBroker:          wsEventBroker,
		conversion:             conversion,
		tgSvc:                  tgSvc,
		wsSvc:                  wsSvc,
		priceStep:              priceStep,
		spreadForStartBuy:      spreadForStartTrade,
		spreadForStartSell:     spreadForStartSell,
		spreadForStopBuyTrade:  spreadForStopBuyTrade,
		spreadForStopSellTrade: spreadForStopSellTrade,
		orderBook:              orderBook,
		orderSvc:               orderSvc,
		logger:                 logger,
	}, nil
}

func (s *Spread) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan os.Signal) {
	defer func() {
		wg.Done()
	}()

	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("spread manager starting...")
	defer s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("spread manager stopped")

	oldSessionChan := make(chan int, 1)

	if len(s.orderBook.Sessions) > 0 {
		oldSessionChan <- 0
	}

	for {
		select {
		case <-oldSessionChan:
			var oldSessWG sync.WaitGroup

			for _, sess := range s.orderBook.Sessions {
				if sess.IsDone.IsNotSet() {
					oldSessWG.Add(1)

					go s.processOldSession(ctx, &oldSessWG, sess, interrupt)
				}
			}

			s.logger.Info().Str("pair", s.pair.GetPairName()).Msg("wait for finishing old sessions")

			oldSessWG.Wait()

			s.logger.Info().Str("pair", s.pair.GetPairName()).Msg("old sessions finished")

		case <-ctx.Done():
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders starting")
			s.cleanUpActiveOrders()
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders finished")

			close(oldSessionChan)

			return
		case <-time.After(sessCreationInterval):
			if s.orderBook.ActiveSessionID.Load() != "" {
				continue
			}

			go s.CreateSession(ctx, interrupt)
		}
	}
}

func (s *Spread) CreateSession(ctx context.Context, interrupt chan os.Signal) {
	sessCtx, cancel := context.WithCancel(ctx)
	sess := s.orderBook.NewSession()

	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("start session")

	s.processSession(sessCtx, sess, interrupt)
	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

	if s.orderBook.ActiveSessionID.Load() == sess.ID {
		s.orderBook.ActiveSessionID.Store("")
	}

	cancel()
}

func (s *Spread) processOldSession(
	ctx context.Context,
	wg *sync.WaitGroup,
	sess *storage.Session,
	interrupt chan os.Signal,
) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("old session started")

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("old session stopped")

	defer wg.Done()

	sessCtx, cancel := context.WithCancel(ctx)

	if sess.GetActiveSellOrderID() != 0 {
		order := s.storage.GetUserOrder(sess.GetActiveSellOrderID())
		if order == nil {
			rid, err := s.wsSvc.GetOrder(sess.GetActiveSellOrderID())
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveSellOrderID()).
					Msg("get order")
				cancel()

				interrupt <- syscall.SIGINT

				return
			}

			o, err := s.orderSvc.UpdateOrderStates(ctx, interrupt, rid)
			if err != nil || o == nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveSellOrderID()).
					Msg("get order by id failed")
				cancel()

				interrupt <- syscall.SIGINT

				return
			}
		}

		go s.manageOrder(ctx, interrupt, sess, sess.GetActiveSellOrderID())
	} else if sess.GetActiveBuyOrderID() != 0 {
		order := s.storage.GetUserOrder(sess.GetActiveBuyOrderID())
		if order == nil {
			reqID, err := s.wsSvc.GetOrder(sess.GetActiveBuyOrderID())
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("get order")
				cancel()

				interrupt <- syscall.SIGINT

				return
			}

			o, err := s.orderSvc.UpdateOrderStates(ctx, interrupt, reqID)
			if err != nil || o == nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("get order by id failed")
				cancel()

				interrupt <- syscall.SIGINT

				return
			}
		}

		go s.manageOrder(ctx, interrupt, sess, sess.GetActiveBuyOrderID())
	} else if sess.GetActiveBuyExtOrderID() != "" {
		rid, err := s.wsSvc.GetOrderByExtID(sess.GetActiveBuyExtOrderID())
		if err != nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveBuyExtOrderID()).
				Msg("get order")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		o, err := s.orderSvc.UpdateOrderStates(ctx, interrupt, rid)
		if err != nil || o == nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveBuyExtOrderID()).
				Msg("get order by ext oid failed")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		sess.SetActiveBuyOrderID(o.ID)
		sess.SetActiveBuyExtOrderID("")
		sess.SetActiveBuyOrderRequestID("")

		go s.manageOrder(ctx, interrupt, sess, sess.GetActiveBuyOrderID())
	} else if sess.GetActiveSellExtOrderID() != "" {
		reqID, err := s.wsSvc.GetOrderByExtID(sess.GetActiveSellExtOrderID())
		if err != nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveSellExtOrderID()).
				Msg("get order")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		o, err := s.orderSvc.UpdateOrderStates(ctx, interrupt, reqID)
		if err != nil || o == nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveSellExtOrderID()).
				Msg("get order by ext oid failed")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		sess.SetActiveSellOrderID(o.ID)
		sess.SetActiveSellExtOrderID("")
		sess.SetActiveSellOrderRequestID("")

		go s.manageOrder(ctx, interrupt, sess, sess.GetActiveSellOrderID())
	}

	s.processSession(sessCtx, sess, interrupt)
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("session finished")

	cancel()
}

func (s *Spread) processSession(
	ctx context.Context,
	sess *storage.Session,
	interrupt chan os.Signal,
) {
	go func() {
		if err := s.listenNewOrders(ctx, interrupt, sess); err != nil {
			s.logger.Err(err).Str("id", sess.ID).Msg("listen new orders")

			interrupt <- syscall.SIGSTOP
		}
	}()

	s.orderCreationDecider(ctx, sess, interrupt)
}

func (s *Spread) orderCreationDecider(ctx context.Context, sess *storage.Session, interrupt chan os.Signal) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("order creation decider starting...")

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("order creation decider stopped")

	e := s.orderBook.EventBroker.Subscribe()
	defer s.orderBook.EventBroker.Unsubscribe(e)

	for {
		select {
		case <-e:
			if sess.IsDone.IsSet() {
				return
			}

			isBuyAvailable, err := s.isBuyOrderCreationAvailable(sess, false)
			if err != nil {
				s.logger.Err(err).Str("id", sess.ID).Msg("is buy order creation available")

				interrupt <- syscall.SIGINT

				return
			}

			if isBuyAvailable {
				if err := s.createBuyOrder(sess); err != nil {
					s.logger.Err(err).Str("id", sess.ID).Msg("create buy order")

					if errors.Is(err, dictionary.ErrInsufficientFunds) {
						continue
					}

					interrupt <- syscall.SIGINT

					return
				}
			}

			isSellAvailable, err := s.isSellOrderCreationAvailable(sess, false)
			if err != nil {
				s.logger.Err(err).Str("id", sess.ID).Msg("is sell order creation available")

				interrupt <- syscall.SIGINT

				return
			}

			if isSellAvailable {
				if err := s.createSellOrder(sess); err != nil {
					s.logger.Err(err).Str("id", sess.ID).Msg("create sell order")

					if errors.Is(err, dictionary.ErrInsufficientFunds) {
						continue
					}

					interrupt <- syscall.SIGINT

					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Spread) isBuyOrderCreationAvailable(sess *storage.Session, force bool) (bool, error) {
	if sess.IsNeedToCreateBuyOrder.IsNotSet() && !force {
		return false, nil
	}

	if s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.orderBook.GetSpread().Cmp(s.spreadForStartBuy) >= 0 {
		buyVolume, _, _, err := s.calculateBuyOrderVolume(sess.GetPrevBuyOrderID())
		if err != nil {
			s.logger.Err(err).Msg("calc buy volume")

			return false, err
		}

		maxBidPrice := s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)
		maxBid := s.orderBook.GetBid(maxBidPrice)

		if maxBid == nil {
			return false, nil
		}

		if maxBid.Amount.Cmp(buyVolume) >= 0 {
			return true, nil
		}
	}

	return false, nil
}

func (s *Spread) isSellOrderCreationAvailable(sess *storage.Session, force bool) (bool, error) {
	if sess.IsNeedToCreateSellOrder.IsNotSet() && !force {
		return false, nil
	}

	if s.calcBuySpread(sess.GetActiveBuyOrderID()).Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.calcBuySpread(sess.GetActiveBuyOrderID()).Cmp(s.spreadForStartSell) == 1 {
		sellVolume, _, _, err := s.calculateSellOrderVolume(sess, sess.GetPrevSellOrderID())
		if err != nil {
			s.logger.Err(err).Msg("calc sell order volume")

			return false, err
		}

		minAskPrice := s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)
		minAsk := s.orderBook.GetAsk(minAskPrice)

		if minAsk == nil {
			return false, nil
		}

		if minAsk.Amount.Cmp(sellVolume) >= 0 {
			return true, nil
		}
	}

	if sess.GetPrevSellOrderID() != 0 && s.orderBook.ActiveSessionID.Load() == sess.ID {
		o := s.storage.GetUserOrder(sess.GetPrevSellOrderID())

		if time.Now().After(o.CreatedTimestamp.Add(time.Hour)) && o.LimitPrice.Cmp(s.orderBook.GetMaxBidPrice()) == -1 {
			s.logger.Warn().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Str("order price", o.LimitPrice.Text('f', s.pair.PriceScale)).
				Str("max bid price", s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)).
				Msg("allow to create new session after 1h of inability to create an order")

			s.tgSvc.Send(fmt.Sprintf(
				`env: %s,
pair: %s,
order price: %s,
min ask price: %s,
allow to create new session after 1h of inability to create an order`,
				s.cfg.Env,
				s.pair.GetPairName(),
				o.LimitPrice.Text('f', s.pair.PriceScale),
				s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale),
			))

			s.orderBook.ActiveSessionID.Store("")
		}
	}

	return false, nil
}

func (s *Spread) listenNewOrders(
	ctx context.Context,
	interrupt chan os.Signal,
	sess *storage.Session,
) error {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("listen new orders subscriber starting...")

	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("listen new orders subscriber stopped")

	eventsCh := s.wsEventBroker.Subscribe()
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
				interrupt <- syscall.SIGSTOP

				return dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}
			err := json.Unmarshal(msg, rid)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")
				interrupt <- syscall.SIGSTOP

				return err
			}

			if sess.GetActiveBuyOrderRequestID() != rid.ID && sess.GetActiveSellOrderRequestID() != rid.ID {
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

			err = json.Unmarshal(msg, co)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			if sess.GetActiveBuyOrderRequestID() == rid.ID {
				sess.SetActiveBuyOrderID(co.OrderID)
				sess.SetActiveBuyExtOrderID("")
				sess.SetActiveBuyOrderRequestID("")
				s.storage.AddBuyOrder(s.pair, sess.ID, co.OrderID)
			}

			if sess.GetActiveSellOrderRequestID() == rid.ID {
				sess.SetActiveSellOrderID(co.OrderID)
				sess.SetActiveSellExtOrderID("")
				sess.SetActiveSellOrderRequestID("")
				s.storage.AddSellOrder(s.pair, sess.ID, co.OrderID)
			}

			go s.manageOrder(ctx, interrupt, sess, co.OrderID)

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Spread) checkListenOrderErrors(msg []byte, sess *storage.Session) (bool, error) {
	er := &response.Error{}

	err := json.Unmarshal(msg, er)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return false, err
	}

	if er.Error != nil {
		// probably prev order executed on max available amount
		if er.Error.Code == response.AmountTooSmallCode &&
			(sess.GetPrevBuyOrderID() != 0 || sess.GetPrevSellOrderID() != 0) {
			if er.ID == sess.GetActiveBuyOrderRequestID() {
				s.logger.Warn().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", sess.GetPrevBuyOrderID()).
					Str("ext oid", sess.GetActiveBuyExtOrderID()).
					Msg("consider prev buy order as executed, allow to place sell order")

				s.setBuyOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevBuyOrderID()))
			} else if er.ID == sess.GetActiveSellOrderRequestID() {
				s.logger.Warn().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", sess.GetPrevSellOrderID()).
					Str("ext oid", sess.GetActiveSellExtOrderID()).
					Msg("consider prev sell order as executed, allow to place buy order")

				s.setSellOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevSellOrderID()))
			}

			return true, nil
		} else if er.Error.Code == response.DoneOrderCode &&
			(sess.GetPrevBuyOrderID() != 0 || sess.GetPrevSellOrderID() != 0) {
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
					sess.IsNeedToCreateBuyOrder.Set()

					return true, nil
				} else if o.State == dictionary.StateDone {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevBuyOrderID()).
						Str("ext oid", sess.GetActiveBuyExtOrderID()).
						Msg("altered buy order already done")

					s.setBuyOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevBuyOrderID()))

					return true, nil
				}
			} else if er.ID == sess.GetActiveSellOrderRequestID() {
				o := s.storage.GetUserOrder(sess.GetPrevSellOrderID())
				if o.State == dictionary.StateCancelled {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevSellOrderID()).
						Str("ext oid", sess.GetActiveSellExtOrderID()).
						Msg("altered sell order already cancelled")

					sess.SetActiveSellExtOrderID("")
					sess.SetActiveSellOrderRequestID("")
					sess.IsNeedToCreateSellOrder.Set()

					return true, nil
				} else if o.State == dictionary.StateDone {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevSellOrderID()).
						Str("ext oid", sess.GetActiveSellExtOrderID()).
						Msg("altered sell order already done")

					s.setSellOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevSellOrderID()))

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

func (s *Spread) createBuyOrder(sess *storage.Session) error {
	sess.IsNeedToCreateBuyOrder.UnSet()

	var err error

	amount, total, price, err := s.calculateBuyOrderVolume(sess.GetPrevBuyOrderID())
	if err != nil {
		s.logger.Err(err).Str("id", sess.ID).Msg("calculate buy order volume")

		return err
	}

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
		sess.IsNeedToCreateBuyOrder.Set()

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
	sess.SetActiveBuyOrderRequestID(strconv.FormatInt(rid, 10))

	return nil
}

func (s *Spread) createSellOrder(sess *storage.Session) error {
	sess.IsNeedToCreateSellOrder.UnSet()

	amount, total, price, err := s.calculateSellOrderVolume(sess, sess.GetPrevSellOrderID())
	if err != nil {
		s.logger.Err(err).Msg("calculate sell order volume")

		return err
	}

	s.logger.
		Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("prev order id", sess.GetPrevSellOrderID()).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Str("spread", s.calcBuySpread(sess.GetActiveBuyOrderID()).String()).
		Msg("time to place sell order")

	if s.storage.GetBalance(s.pair.BaseCurrency).Available.Cmp(amount) == -1 {
		sess.IsNeedToCreateSellOrder.Set()

		return dictionary.ErrInsufficientFunds
	}

	rid, extID, err := s.wsSvc.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.SellBase,
	)
	if err != nil {
		s.logger.Err(err).Msg("create sell order")

		return err
	}

	sess.SetActiveSellExtOrderID(extID)
	sess.SetActiveSellOrderRequestID(strconv.FormatInt(rid, 10))

	return nil
}

func (s *Spread) manageOrder(ctx context.Context, interrupt chan os.Signal, sess *storage.Session, orderID int64) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("start order manager process")

	e := s.orderBook.EventBroker.Subscribe()

	defer s.orderBook.EventBroker.Unsubscribe(e)
	defer s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("stop order manager process")

	startedTime := time.Now()

	for {
		select {
		case <-e:
			// order sent, wait creation
			order := s.storage.GetUserOrder(orderID)
			if order == nil {
				s.logger.Warn().Int64("oid", orderID).Msg("order not found")

				if startedTime.Add(time.Minute).Before(time.Now()) {
					s.logger.Error().Msg("order creation event not received")

					interrupt <- syscall.SIGSTOP

					return
				}

				continue
			}

			if order.State < dictionary.StateActive {
				s.logger.Warn().Int64("oid", orderID).Msg("order state is below active")

				continue
			}

			// stop manage order if executed
			if order.State > dictionary.StateActive {
				if order.TradeIntent == dictionary.BuyBase {
					s.logger.Warn().
						Str("id", sess.ID).
						Int("state", order.State).
						Int64("oid", orderID).
						Msg("buy order reached final state")

					if order.State == dictionary.StateDone {
						s.setBuyOrderExecutedFlags(sess, order)

						return
					}

					if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						sess.SetPrevBuyOrderID(orderID)
						sess.SetActiveBuyOrderID(0)
						sess.IsNeedToCreateBuyOrder.Set()

						return
					}
				} else if order.TradeIntent == dictionary.SellBase {
					s.logger.Warn().
						Str("id", sess.ID).
						Int("state", order.State).
						Int64("oid", orderID).
						Msg("sell order reached final state")

					if order.State == dictionary.StateDone {
						s.setSellOrderExecutedFlags(sess, order)
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						sess.SetActiveSellOrderID(0)
						sess.IsNeedToCreateSellOrder.Set()
					}
				}

				return
			}

			if order.TradeIntent == dictionary.BuyBase {
				spread := s.calcBuySpread(sess.GetActiveBuyOrderID())
				// cancel buy order
				if spread.Cmp(s.spreadForStopBuyTrade) == -1 && s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel buy order")

					err := s.cancelOrder(orderID)
					if err != nil {
						if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
							s.logger.Warn().
								Str("id", sess.ID).
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.orderBook.EventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().
							Str("id", sess.ID).
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("cancel buy order")
					}

					sess.SetPrevBuyOrderID(orderID)
					sess.SetActiveBuyOrderID(0)
					sess.IsNeedToCreateBuyOrder.Set()

					return
				}

				if s.isMoveBuyOrderRequired(sess, order) {
					isBuyAvailable, err := s.isBuyOrderCreationAvailable(sess, true)
					if err != nil {
						s.logger.Fatal().
							Err(err).
							Str("id", sess.ID).
							Int64("oid", order.ID).
							Msg("is buy order creation available")
					}

					if isBuyAvailable {
						rid, extID, err := s.moveBuyOrder(orderID)
						if err != nil {
							s.logger.Fatal().
								Err(err).
								Str("id", sess.ID).
								Int64("oid", order.ID).
								Msg("move buy order")
						}

						sess.SetPrevBuyOrderID(orderID)
						sess.SetActiveBuyExtOrderID(extID)
						sess.SetActiveBuyOrderRequestID(strconv.FormatInt(rid, 10))

						s.orderBook.EventBroker.Publish(0) // don't wait change order book
					} else {
						err := s.cancelOrder(orderID)
						if err != nil {
							if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
								s.logger.Warn().
									Str("id", sess.ID).
									Str("pair", s.pair.GetPairName()).
									Int64("oid", orderID).
									Msg("expected cancelled buy order state, but got done")

								s.orderBook.EventBroker.Publish(0)

								continue
							}

							s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order for move")
						}

						sess.SetPrevBuyOrderID(orderID)
						sess.SetActiveBuyOrderID(0)
						sess.IsNeedToCreateBuyOrder.Set()

						s.orderBook.EventBroker.Publish(0) // don't wait change order book
					}

					return
				}
			}

			if order.TradeIntent == dictionary.SellBase {
				spread := s.calcBuySpread(sess.GetActiveBuyOrderID())

				// cancel sell order
				if spread.Cmp(s.spreadForStopBuyTrade) == -1 {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel sell order")

					err := s.cancelOrder(orderID)
					if err != nil {
						if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
							s.logger.Warn().
								Str("id", sess.ID).
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled sell order state, but got done")

							s.orderBook.EventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().
							Str("id", sess.ID).
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("can't cancel sell order")
					}

					sess.SetPrevSellOrderID(orderID)
					sess.SetActiveSellOrderID(0)
					sess.IsNeedToCreateSellOrder.Set()

					return
				}

				if s.isMoveSellOrderRequired(sess, order) {
					isSellAvailable, err := s.isSellOrderCreationAvailable(sess, true)
					if err != nil {
						s.logger.Fatal().Err(err).Msg("is sell order creation available")
					}

					if isSellAvailable {
						rid, extID, err := s.moveSellOrder(sess, orderID)
						if err != nil {
							s.logger.Fatal().Err(err).Msg("move sell order")
						}

						sess.SetPrevSellOrderID(orderID)
						sess.SetActiveSellExtOrderID(extID)
						sess.SetActiveSellOrderRequestID(strconv.FormatInt(rid, 10))

						s.orderBook.EventBroker.Publish(0) // don't wait change order book
					} else {
						err := s.cancelOrder(orderID)
						if err != nil {
							if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
								s.logger.Warn().
									Str("id", sess.ID).
									Str("pair", s.pair.GetPairName()).
									Int64("oid", orderID).
									Msg("expected cancelled sell order state, but got done")

								s.orderBook.EventBroker.Publish(0)

								continue
							}

							s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
						}

						sess.SetPrevSellOrderID(orderID)
						sess.SetActiveSellOrderID(0)
						sess.IsNeedToCreateSellOrder.Set()

						s.orderBook.EventBroker.Publish(0)
					}

					return
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

func (s *Spread) calcBuySpread(activeBuyOrderID int64) *big.Float {
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

func (s *Spread) cancelOrder(orderID int64) error {
	eventsCh := s.wsEventBroker.Subscribe()
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.CancelOrder(orderID)
	if err != nil {
		s.logger.Fatal().Int64("oid", orderID).Msg("cancel order")
	}

	for e := range eventsCh {
		msg, ok := e.([]byte)
		if !ok {
			return dictionary.ErrCantConvertInterfaceToBytes
		}

		rid := &response.ID{}
		err := json.Unmarshal(msg, rid)

		if err != nil {
			s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

			return err
		}

		if strconv.FormatInt(id, 10) != rid.ID {
			continue
		}

		s.logger.Info().
			Str("pair", s.pair.GetPairName()).
			Int64("oid", orderID).
			Bytes("payload", msg).
			Msg("cancel order response received")

		er := &response.Error{}

		err = json.Unmarshal(msg, er)
		if err != nil {
			s.logger.Fatal().Err(err).Msg("unmarshall")
		}

		if er.Error != nil {
			if er.Error.Reason != response.CancelledOrder {
				if er.Error.Code == response.DoneOrderCode {
					s.logger.Err(err).Bytes("response", msg).Msg("can't cancel order")

					return dictionary.ErrCantCancelDoneOrder
				}

				err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
				s.logger.Err(err).Bytes("response", msg).Msg("can't cancel order")

				return err
			}
		}

		return nil
	}

	return nil
}

func (s *Spread) moveBuyOrder(orderID int64) (int64, string, error) {
	amount, total, price, err := s.calculateBuyOrderVolume(orderID)
	if err != nil {
		s.logger.Err(err).Msg("calculate buy order volume for alter order")

		return 0, "", err
	}

	id, extID, err := s.wsSvc.AlterOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.BuyBase,
		orderID,
	)
	if err != nil {
		s.logger.Err(err).
			Str("pair", s.pair.GetPairName()).
			Str("ext oid", extID).
			Int64("prev order id", orderID).
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
		Int64("prev order id", orderID).
		Str("spread", s.orderBook.GetSpread().Text('f', s.pair.PriceScale)).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Msg("alter buy order")

	return id, extID, nil
}

func (s *Spread) moveSellOrder(sess *storage.Session, orderID int64) (int64, string, error) {
	amount, total, price, err := s.calculateSellOrderVolume(sess, orderID)
	if err != nil {
		s.logger.Err(err).Msg("calculate buy order volume for alter order")

		return 0, "", err
	}

	id, extID, err := s.wsSvc.AlterOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.SellBase,
		orderID,
	)
	if err != nil {
		s.logger.
			Warn().
			Str("pair", s.pair.GetPairName()).
			Int64("prev order id", orderID).
			Str("price", price.Text('f', s.pair.PriceScale)).
			Str("amount", amount.Text('f', s.pair.QuantityScale)).
			Str("total", total.String()).
			Str("spread", s.calcBuySpread(sess.GetActiveBuyOrderID()).String()).
			Msg("alter sell order")

		return 0, "", err
	}

	s.logger.
		Warn().
		Str("pair", s.pair.GetPairName()).
		Str("ext oid", extID).
		Int64("prev order id", orderID).
		Str("spread", s.orderBook.GetSpread().Text('f', s.pair.PriceScale)).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Msg("alter sell order")

	return id, extID, nil
}

func (s *Spread) setBuyOrderExecutedFlags(sess *storage.Session, order *storage.Order) {
	atomic.AddInt64(&s.orderBook.CompletedBuyOrders, 1)
	sess.SetPrevBuyOrderID(0)
	sess.SetActiveBuyOrderID(order.ID)
	sess.SetActiveBuyExtOrderID("")
	sess.SetActiveBuyOrderRequestID("")
	sess.SetActiveSellOrderID(0)
	sess.SetActiveSellExtOrderID("")
	sess.SetActiveSellOrderRequestID("")
	sess.IsNeedToCreateSellOrder.Set()

	totalBoughtVolume, ok := s.storage.GetTotalBuyVolume(s.pair, sess.ID)
	if !ok {
		s.logger.Warn().Msg("parse string as float")
	}

	s.tgSvc.Send(fmt.Sprintf(
		`env: %s,
buy order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		totalBoughtVolume.Text('f', s.pair.QuantityScale),
	))
}

func (s *Spread) setSellOrderExecutedFlags(sess *storage.Session,
	order *storage.Order) {
	atomic.AddInt64(&s.orderBook.CompletedSellOrders, 1)
	sess.SetPrevSellOrderID(0)
	sess.SetActiveSellOrderID(0)

	sess.IsDone.Set()

	totalSoldVolume, ok := s.storage.GetTotalSellVolume(s.pair, sess.ID)
	if !ok {
		s.logger.Warn().Msg("parse string as float")
	}

	s.tgSvc.Send(fmt.Sprintf(
		`env: %s,
sell order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		totalSoldVolume.Text('f', s.pair.QuantityScale),
	))
}

func (s *Spread) cleanUpActiveOrders() {
	for _, sess := range s.orderBook.Sessions {
		if sess.IsDone.IsSet() {
			continue
		}

		if sess.GetActiveBuyOrderID() > 1 {
			if o := s.storage.GetUserOrder(sess.GetActiveBuyOrderID()); o != nil &&
				o.State < dictionary.StateDone {
				_, err := s.wsSvc.CancelOrder(sess.GetActiveBuyOrderID())
				if err != nil {
					s.logger.Fatal().
						Int64("oid", sess.GetActiveBuyOrderID()).
						Msg("send cancel buy order request")
				}

				s.logger.Warn().
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("send cancel buy order request")
			}
		}

		if sess.GetActiveSellOrderID() > 1 {
			if o := s.storage.GetUserOrder(sess.GetActiveSellOrderID()); o != nil &&
				o.State < dictionary.StateDone {
				_, err := s.wsSvc.CancelOrder(sess.GetActiveSellOrderID())
				if err != nil {
					s.logger.
						Fatal().
						Int64("oid", sess.GetActiveSellOrderID()).
						Msg("send cancel sell order request")
				}

				s.logger.Warn().
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("send cancel sell order request")
			}
		}
	}
}

func (s *Spread) getTotalBuyVolume() (*big.Float, error) {
	totalBuyInUSDT, ok := big.NewFloat(0).SetString(s.cfg.TotalBuyInUSDT)
	if !ok {
		s.logger.Err(dictionary.ErrParseFloat).Str("val", s.cfg.TotalBuyInUSDT).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	quotedToUSDTPrices, err := s.conversion.GetUSDTPrice(s.pair.QuoteCurrency)
	if err != nil {
		return nil, err
	}

	totalBuyVolumeInQuoted := big.NewFloat(0).Quo(totalBuyInUSDT, quotedToUSDTPrices)

	minVolume, ok := big.NewFloat(0).SetString(s.pair.MinVolume)
	if !ok {
		s.logger.Err(dictionary.ErrParseFloat).Str("val", s.cfg.TotalBuyInUSDT).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	if totalBuyVolumeInQuoted.Cmp(minVolume) == -1 {
		return minVolume, err
	}

	return totalBuyVolumeInQuoted, nil
}

func (s *Spread) calculateBuyOrderVolume(prevOrderID int64) (
	amount *big.Float,
	total *big.Float,
	price *big.Float,
	err error,
) {
	price = big.NewFloat(0).Add(s.orderBook.GetMaxBidPrice(), s.priceStep)
	total = big.NewFloat(0)
	amount = big.NewFloat(0)

	if prevOrderID == 0 {
		total, err = s.getTotalBuyVolume()
		if err != nil {
			s.logger.Err(err).Msg("parse string as float")

			return nil, nil, nil, err
		}

		amount.Quo(total, price)

		return amount, total, price, nil
	}

	prevBuyOrder := s.storage.GetUserOrder(prevOrderID)

	orderedTotal := big.NewFloat(0).Mul(prevBuyOrder.OrderedVolume, prevBuyOrder.LimitPrice)

	if prevBuyOrder.TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) != 0 {
		soldTotal := big.NewFloat(0).Mul(prevBuyOrder.TotalBuyVolume, prevBuyOrder.LimitPrice)

		total.Sub(orderedTotal, soldTotal)
		amount.Quo(total, price)
	} else {
		amount = amount.Quo(orderedTotal, price)
		total.Mul(amount, price)
	}

	return amount, total, price, nil
}

func (s *Spread) calculateSellOrderVolume(sess *storage.Session, prevOrderID int64) (
	amount *big.Float,
	total *big.Float,
	price *big.Float,
	err error,
) {
	price = big.NewFloat(0).Sub(s.orderBook.GetMinAskPrice(), s.priceStep)
	total = big.NewFloat(0)

	var ok bool

	if prevOrderID == 0 {
		amount, ok = s.storage.GetTotalBuyVolume(s.pair, sess.ID)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Int64("prev oid", prevOrderID).
				Msg("parse string as float")

			return nil, nil, nil, dictionary.ErrParseFloat
		}
	} else {
		prevSellOrder := s.storage.GetUserOrder(prevOrderID)
		amount = prevSellOrder.OrderedVolume

		if prevSellOrder.TotalSellVolume.Cmp(dictionary.ZeroBigFloat) != 0 {
			newAmount := amount.Sub(amount, prevSellOrder.TotalSellVolume)
			amount = newAmount
		}
	}

	total.Mul(amount, price)

	return amount, total, price, nil
}

func (s *Spread) isMoveBuyOrderRequired(sess *storage.Session, o *storage.Order) bool {
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

func (s *Spread) isMoveSellOrderRequired(sess *storage.Session, o *storage.Order) bool {
	previousPossiblePrice := big.NewFloat(0).Add(o.LimitPrice, s.priceStep)

	prevPriceExists := s.orderBook.GetAsk(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
	nextPriceExists := s.orderBook.GetMinAskPrice().Cmp(o.LimitPrice) == -1

	isReq := nextPriceExists || !prevPriceExists

	if !isReq {
		return false
	}

	spread := s.calcBuySpread(sess.GetActiveBuyOrderID())

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
