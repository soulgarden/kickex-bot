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
	"syscall"
	"time"

	spreadSvc "github.com/soulgarden/kickex-bot/service/spread"
	storageSpread "github.com/soulgarden/kickex-bot/storage/spread"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

const sessCreationInterval = 10 * time.Millisecond
const orderCreationDuration = time.Second * 10

type Spread struct {
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
	sessSvc        *spreadSvc.Session

	priceStep              *big.Float
	spreadForStartBuy      *big.Float
	spreadForStartSell     *big.Float
	spreadForStopBuyTrade  *big.Float
	spreadForStopSellTrade *big.Float
	totalBuyInUSDT         *big.Float

	logger *zerolog.Logger
}

func NewSpread(
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
	sessSvc *spreadSvc.Session,
	logger *zerolog.Logger,
) (*Spread, error) {
	zeroStep := big.NewFloat(0).Text('f', pair.PriceScale)
	priceStepStr := zeroStep[0:pair.PriceScale+1] + "1"

	priceStep, ok := big.NewFloat(0).SetPrec(uint(pair.PriceScale)).SetString(priceStepStr)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartTrade, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStartBuy)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartSell, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStartSell)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopBuyTrade, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStopBuyTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopSellTrade, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStopSellTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	totalBuyInUSDT, ok := big.NewFloat(0).SetString(cfg.Spread.TotalBuyInUSDT)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", cfg.Spread.TotalBuyInUSDT).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Spread{
		cfg:                    cfg,
		pair:                   pair,
		storage:                storage,
		wsEventBroker:          wsEventBroker,
		accEventBroker:         accEventBroker,
		conversion:             conversion,
		tgSvc:                  tgSvc,
		wsSvc:                  wsSvc,
		priceStep:              priceStep,
		spreadForStartBuy:      spreadForStartTrade,
		spreadForStartSell:     spreadForStartSell,
		spreadForStopBuyTrade:  spreadForStopBuyTrade,
		spreadForStopSellTrade: spreadForStopSellTrade,
		totalBuyInUSDT:         totalBuyInUSDT,
		orderBook:              orderBook,
		orderSvc:               orderSvc,
		sessSvc:                sessSvc,
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

	if len(s.orderBook.SpreadSessions) > 0 {
		oldSessionChan <- 0
	}

	for {
		select {
		case <-oldSessionChan:
			var oldSessWG sync.WaitGroup

			for _, sess := range s.orderBook.SpreadSessions {
				if !sess.GetIsDone() {
					oldSessWG.Add(1)

					go s.processOldSession(ctx, &oldSessWG, sess, interrupt)
				}
			}

			s.logger.Info().Str("pair", s.pair.GetPairName()).Msg("wait for finishing old sessions")

			oldSessWG.Wait()

			s.logger.Info().Str("pair", s.pair.GetPairName()).Msg("old sessions finished")

		case <-ctx.Done():
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders starting")
			s.cleanUpBeforeShutdown()
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders finished")

			close(oldSessionChan)

			return
		case <-time.After(sessCreationInterval):
			if s.orderBook.SpreadActiveSessionID.Load() != "" {
				continue
			}

			go s.CreateSession(ctx, interrupt)
		}
	}
}

func (s *Spread) CreateSession(ctx context.Context, interrupt chan os.Signal) {
	volume, err := s.getStartBuyVolume()
	if err != nil {
		s.logger.Err(err).Msg("get start buy volume")

		interrupt <- syscall.SIGSTOP

		return
	}

	sessCtx, cancel := context.WithCancel(ctx)

	sess := s.orderBook.NewSpreadSession(volume)

	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("start session")

	s.processSession(sessCtx, sess, interrupt)
	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

	if s.orderBook.SpreadActiveSessionID.Load() == sess.ID {
		s.orderBook.SpreadActiveSessionID.Store("")
	}

	cancel()
}

func (s *Spread) processOldSession(
	ctx context.Context,
	wg *sync.WaitGroup,
	sess *storageSpread.Session,
	interrupt chan os.Signal,
) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Str("active sell ext oid", sess.ActiveSellExtOrderID).
		Str("active buy ext oid", sess.ActiveBuyExtOrderID).
		Int64("active buy oid", sess.ActiveBuyOrderID).
		Int64("active sell oid", sess.ActiveSellOrderID).
		Int64("prev buy oid", sess.PrevBuyOrderID).
		Int64("prev sell oid", sess.PrevSellOrderID).
		Bool("is need to create buy order", sess.IsNeedToCreateBuyOrder).
		Bool("is need to create sell order", sess.IsNeedToCreateSellOrder).
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
			err := s.updateOrderStateByID(ctx, interrupt, sess.GetActiveSellOrderID())
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveSellOrderID()).
					Msg("update sell order state by id")

				cancel()

				interrupt <- syscall.SIGINT

				return
			}
		}

		go s.watchOrder(ctx, interrupt, sess, sess.GetActiveSellOrderID())
	} else if sess.GetActiveBuyOrderID() != 0 {
		order := s.storage.GetUserOrder(sess.GetActiveBuyOrderID())
		if order == nil {
			err := s.updateOrderStateByID(ctx, interrupt, sess.GetActiveBuyOrderID())
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("update buy order state by id")

				cancel()

				interrupt <- syscall.SIGINT

				return
			}
		}

		go s.watchOrder(ctx, interrupt, sess, sess.GetActiveBuyOrderID())
	} else if sess.GetActiveBuyExtOrderID() != "" {
		o, err := s.updateOrderStateByExtID(ctx, interrupt, sess.GetActiveBuyExtOrderID())
		if err != nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveBuyExtOrderID()).
				Int64("oid", sess.GetActiveBuyOrderID()).
				Msg("update buy order state by ext id")

			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		sess.SetActiveBuyOrderID(o.ID)
		sess.SetActiveBuyExtOrderID("")
		sess.SetActiveBuyOrderRequestID("")

		go s.watchOrder(ctx, interrupt, sess, sess.GetActiveBuyOrderID())
	} else if sess.GetActiveSellExtOrderID() != "" {
		o, err := s.updateOrderStateByExtID(ctx, interrupt, sess.GetActiveSellExtOrderID())
		if err != nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveSellExtOrderID()).
				Int64("oid", sess.GetActiveSellOrderID()).
				Msg("update sell order state by ext id")

			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		sess.SetActiveSellOrderID(o.ID)
		sess.SetActiveSellExtOrderID("")
		sess.SetActiveSellOrderRequestID("")

		go s.watchOrder(ctx, interrupt, sess, sess.GetActiveSellOrderID())
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
	sess *storageSpread.Session,
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

func (s *Spread) orderCreationDecider(ctx context.Context, sess *storageSpread.Session, interrupt chan os.Signal) {
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
			if sess.GetIsDone() {
				return
			}

			isBuyAvailable := s.isBuyOrderCreationAvailable(sess, false)

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

			isSellAvailable := s.isSellOrderCreationAvailable(sess, false)

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

func (s *Spread) isBuyOrderCreationAvailable(sess *storageSpread.Session, force bool) bool {
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

func (s *Spread) isSellOrderCreationAvailable(sess *storageSpread.Session, force bool) bool {
	if !sess.GetIsNeedToCreateSellOrder() && !force {
		return false
	}

	if s.calcBuySpread(sess.GetActiveBuyOrderID()).Cmp(dictionary.ZeroBigFloat) == 1 &&
		s.calcBuySpread(sess.GetActiveBuyOrderID()).Cmp(s.spreadForStartSell) == 1 {
		sellVolume, _, _ := s.calculateSellOrderVolume(sess)

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

			s.orderBook.SpreadActiveSessionID.Store("")
		}
	}

	return false
}

func (s *Spread) listenNewOrders(
	ctx context.Context,
	interrupt chan os.Signal,
	sess *storageSpread.Session,
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
				sess.AddBuyOrder(co.OrderID)
			}

			if sess.GetActiveSellOrderRequestID() == rid.ID {
				sess.SetActiveSellOrderID(co.OrderID)
				sess.SetActiveSellExtOrderID("")
				sess.SetActiveSellOrderRequestID("")
				sess.AddSellOrder(co.OrderID)
			}

			go s.watchOrder(ctx, interrupt, sess, co.OrderID)

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Spread) checkListenOrderErrors(msg []byte, sess *storageSpread.Session) (isSkipRequired bool, err error) {
	er := &response.Error{}

	err = json.Unmarshal(msg, er)
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
					sess.SetIsNeedToCreateBuyOrder(true)

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
					sess.SetIsNeedToCreateSellOrder(true)

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

func (s *Spread) createBuyOrder(sess *storageSpread.Session) error {
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
	sess.SetActiveBuyOrderRequestID(strconv.FormatInt(rid, 10))

	return nil
}

func (s *Spread) createSellOrder(sess *storageSpread.Session) error {
	sess.SetIsNeedToCreateSellOrder(false)

	amount, total, price := s.calculateSellOrderVolume(sess)

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
		sess.SetIsNeedToCreateSellOrder(true)

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

func (s *Spread) watchOrder(ctx context.Context, interrupt chan os.Signal, sess *storageSpread.Session, orderID int64) {
	s.logger.Warn().
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("start watch order process")

	bookEventCh := s.orderBook.EventBroker.Subscribe()
	accEventCh := s.accEventBroker.Subscribe()

	defer s.orderBook.EventBroker.Unsubscribe(bookEventCh)
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

				interrupt <- syscall.SIGSTOP

				return
			}

			hasFinalState, err := s.checkOrderState(ctx, interrupt, orderID, sess, &startedTime)
			if err != nil {
				interrupt <- syscall.SIGSTOP

				return
			}

			if hasFinalState {
				return
			}
		case <-bookEventCh:
			hasFinalState, err := s.checkOrderState(ctx, interrupt, orderID, sess, &startedTime)
			if err != nil {
				interrupt <- syscall.SIGSTOP

				return
			}

			if hasFinalState {
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

func (s *Spread) checkOrderState(
	ctx context.Context,
	interrupt chan os.Signal,
	orderID int64,
	sess *storageSpread.Session,
	startedTime *time.Time,
) (hasFinalState bool, err error) {
	// order sent, wait creation
	order := s.storage.GetUserOrder(orderID)
	if order == nil {
		s.logger.Warn().Int64("oid", orderID).Msg("order not found")

		if startedTime.Add(orderCreationDuration).Before(time.Now()) {
			s.logger.Err(dictionary.ErrOrderCreationEventNotReceived).Msg("order creation event not received")

			err := s.updateOrderStateByID(ctx, interrupt, orderID)
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("update buy order state by id")

				interrupt <- syscall.SIGINT

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
		if order.TradeIntent == dictionary.BuyBase {
			s.logger.Warn().
				Str("id", sess.ID).
				Int("state", order.State).
				Int64("oid", orderID).
				Msg("buy order reached final state")

			if order.State == dictionary.StateDone {
				s.setBuyOrderExecutedFlags(sess, order)

				return true, nil
			}

			if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
				sess.SetPrevBuyOrderID(orderID)
				sess.SetActiveBuyOrderID(0)
				sess.SetIsNeedToCreateBuyOrder(true)

				return true, nil
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
				sess.SetIsNeedToCreateSellOrder(true)
			}
		}

		return true, nil
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

			err := s.orderSvc.CancelOrder(orderID)
			if err != nil {
				if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Msg("expected cancelled state, but got done")

					s.orderBook.EventBroker.Publish(0)

					return false, nil
				}

				s.logger.Fatal().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("oid", orderID).
					Msg("cancel buy order")
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
				err := s.orderSvc.CancelOrder(orderID)
				if err != nil {
					if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
						s.logger.Warn().
							Str("id", sess.ID).
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("expected cancelled buy order state, but got done")

						s.orderBook.EventBroker.Publish(0)

						return false, nil
					}

					s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order for move")
				}

				sess.SetPrevBuyOrderID(orderID)
				sess.SetActiveBuyOrderID(0)
				sess.SetIsNeedToCreateBuyOrder(true)

				s.orderBook.EventBroker.Publish(0) // don't wait change order book
			}

			return true, nil
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

			err := s.orderSvc.CancelOrder(orderID)
			if err != nil {
				if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Msg("expected cancelled sell order state, but got done")

					s.orderBook.EventBroker.Publish(0)

					return false, nil
				}

				s.logger.Fatal().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("oid", orderID).
					Msg("can't cancel sell order")
			}

			sess.SetPrevSellOrderID(orderID)
			sess.SetActiveSellOrderID(0)
			sess.SetIsNeedToCreateSellOrder(true)

			return true, nil
		}

		if s.isMoveSellOrderRequired(sess, order) {
			isSellAvailable := s.isSellOrderCreationAvailable(sess, true)

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
				err := s.orderSvc.CancelOrder(orderID)
				if err != nil {
					if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
						s.logger.Warn().
							Str("id", sess.ID).
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("expected cancelled sell order state, but got done")

						s.orderBook.EventBroker.Publish(0)

						return false, nil
					}

					s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
				}

				sess.SetPrevSellOrderID(orderID)
				sess.SetActiveSellOrderID(0)
				sess.SetIsNeedToCreateSellOrder(true)

				s.orderBook.EventBroker.Publish(0)
			}

			return true, nil
		}
	}

	return false, nil
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

func (s *Spread) moveBuyOrder(sess *storageSpread.Session) (int64, string, error) {
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

func (s *Spread) moveSellOrder(sess *storageSpread.Session, orderID int64) (int64, string, error) {
	amount, total, price := s.calculateSellOrderVolume(sess)

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

func (s *Spread) setBuyOrderExecutedFlags(sess *storageSpread.Session, order *storage.Order) {
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

			s.logger.Fatal().
				Err(err).
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", order.ID).
				Msg("cancel buy order")
		}
	}

	sess.SetBuyOrderExecutedFlags(order.ID, s.sessSvc.GetSessTotalBoughtVolume(sess))

	s.orderBook.SubProfit(s.sessSvc.GetSessTotalBoughtCost(sess))
	s.orderBook.EventBroker.Publish(0)

	s.tgSvc.Send(fmt.Sprintf(
		`env: %s,
buy order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s,
cost: %s,
total profit: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		s.sessSvc.GetSessTotalBoughtVolume(sess).Text('f', s.pair.QuantityScale),
		s.sessSvc.GetSessTotalBoughtCost(sess).Text('f', s.pair.PriceScale),
		s.orderBook.GetProfit().Text('f', s.pair.PriceScale),
	))
}

func (s *Spread) setSellOrderExecutedFlags(sess *storageSpread.Session, order *storage.Order) {
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

			s.logger.Fatal().
				Str("id", sess.ID).
				Str("pair", s.pair.GetPairName()).
				Int64("oid", order.ID).
				Msg("cancel sell order")
		}
	}

	sess.SetSellOrderExecutedFlags()

	s.orderBook.AddProfit(s.sessSvc.GetSessTotalSoldCost(sess))
	s.orderBook.EventBroker.Publish(0)

	soldVolume := s.sessSvc.GetSessTotalSoldVolume(sess)
	boughtVolume := s.sessSvc.GetSessTotalBoughtVolume(sess)

	s.tgSvc.Send(fmt.Sprintf(
		`env: %s,
sell order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s,
cost: %s,
profit: %s,
total profit: %s,
not sold volume: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		soldVolume.Text('f', s.pair.QuantityScale),
		s.sessSvc.GetSessTotalSoldCost(sess).Text('f', s.pair.PriceScale),
		s.sessSvc.GetSessProfit(sess).Text('f', s.pair.PriceScale),
		s.orderBook.GetProfit().Text('f', s.pair.PriceScale),
		big.NewFloat(0).Sub(boughtVolume, soldVolume).Text('f', s.pair.QuantityScale),
	))
}

func (s *Spread) cleanUpBeforeShutdown() {
	for _, sess := range s.orderBook.SpreadSessions {
		if sess.GetIsDone() {
			continue
		}

		if sess.GetActiveBuyOrderID() > 1 {
			if o := s.storage.GetUserOrder(sess.GetActiveBuyOrderID()); o != nil && o.State < dictionary.StateDone {
				s.orderSvc.SendCancelOrderRequest(sess.GetActiveBuyOrderID())
			}
		}

		if sess.GetActiveSellOrderID() > 1 {
			if o := s.storage.GetUserOrder(sess.GetActiveSellOrderID()); o != nil && o.State < dictionary.StateDone {
				s.orderSvc.SendCancelOrderRequest(sess.GetActiveSellOrderID())
			}
		}
	}
}

func (s *Spread) getStartBuyVolume() (*big.Float, error) {
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

func (s *Spread) calculateBuyOrderVolume(sess *storageSpread.Session) (amount, total, price *big.Float) {
	price = big.NewFloat(0).Add(s.orderBook.GetMaxBidPrice(), s.priceStep)
	total = big.NewFloat(0).Sub(sess.GetBuyTotal(), s.sessSvc.GetSessTotalBoughtCost(sess))
	amount = big.NewFloat(0)

	amount.Quo(total, price)

	return amount, total, price
}

func (s *Spread) calculateSellOrderVolume(sess *storageSpread.Session) (amount, total, price *big.Float) {
	price = big.NewFloat(0).Sub(s.orderBook.GetMinAskPrice(), s.priceStep)
	total = big.NewFloat(0)
	amount = big.NewFloat(0)

	amount.Sub(sess.GetSellVolume(), s.sessSvc.GetSessTotalSoldVolume(sess))

	total.Mul(amount, price)

	return amount, total, price
}

func (s *Spread) isMoveBuyOrderRequired(sess *storageSpread.Session, o *storage.Order) bool {
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

func (s *Spread) isMoveSellOrderRequired(sess *storageSpread.Session, o *storage.Order) bool {
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

func (s *Spread) updateOrderStateByID(ctx context.Context, interrupt chan os.Signal, oid int64) error {
	rid, err := s.wsSvc.GetOrder(oid)
	if err != nil {
		s.logger.Err(err).Int64("oid", oid).Msg("send get order by id request")

		return err
	}

	o, err := s.orderSvc.UpdateOrderState(ctx, interrupt, rid)
	if err != nil || o == nil {
		s.logger.Err(err).
			Int64("oid", oid).
			Msg("get order by id failed")

		return err
	}

	return nil
}

func (s *Spread) updateOrderStateByExtID(
	ctx context.Context,
	interrupt chan os.Signal,
	extID string,
) (*storage.Order, error) {
	rid, err := s.wsSvc.GetOrderByExtID(extID)
	if err != nil {
		s.logger.Err(err).
			Str("ext oid", extID).
			Msg("send get order by ext id request")

		return nil, err
	}

	o, err := s.orderSvc.UpdateOrderState(ctx, interrupt, rid)
	if err != nil || o == nil {
		s.logger.Err(err).
			Str("ext oid", extID).
			Msg("get order by ext id failed")

		return nil, err
	}

	return o, nil
}
