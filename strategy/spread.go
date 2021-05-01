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
			var sess *storage.Session

			for _, sess = range s.orderBook.Sessions {
				if sess.IsDone.IsNotSet() {
					s.processOldSession(ctx, sess, interrupt)
				}
			}

		case <-ctx.Done():
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders starting")
			s.cleanUpActiveOrders()
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders finished")

			close(oldSessionChan)

			return
		case <-time.After(time.Millisecond):
			sessCtx, cancel := context.WithCancel(ctx)
			sess := s.orderBook.NewSession()

			s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("start session")

			s.processSession(sessCtx, sess, interrupt)
			s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

			cancel()
		}
	}
}

func (s *Spread) processOldSession(
	ctx context.Context,
	sess *storage.Session,
	interrupt chan os.Signal,
) {
	s.logger.Warn().Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("old session started")

	defer s.logger.Warn().Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("old session stopped")

	sessCtx, cancel := context.WithCancel(ctx)

	if sess.ActiveSellOrderID != 0 {
		order := s.storage.GetUserOrder(sess.ActiveSellOrderID)
		if order == nil {
			s.logger.Error().
				Int64("oid", sess.ActiveSellOrderID).
				Msg("active sell order not fount in order list")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		go s.manageOrder(ctx, sess, sess.ActiveSellOrderID)
	} else if sess.ActiveBuyOrderID != 0 {
		order := s.storage.GetUserOrder(sess.ActiveBuyOrderID)
		if order == nil {
			s.logger.Error().
				Int64("oid", sess.ActiveBuyOrderID).
				Msg("active buy order not fount in order list")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		go s.manageOrder(ctx, sess, sess.ActiveBuyOrderID)
	} else if sess.ActiveBuyExtOrderID != 0 {
		id, err := s.wsSvc.GetOrder(sess.ActiveBuyExtOrderID)
		if err != nil {
			s.logger.Err(err).
				Int64("oid", sess.ActiveBuyExtOrderID).
				Msg("get order")
			cancel()

			interrupt <- syscall.SIGINT
		}

		err = s.orderSvc.UpdateOrderStates(ctx, interrupt, id)
		if err != nil {
			s.logger.Err(err).
				Int64("oid", sess.ActiveBuyExtOrderID).
				Msg("get order by ext id")
			cancel()

			interrupt <- syscall.SIGINT
		}

		order := s.storage.GetUserOrder(sess.ActiveBuyExtOrderID)
		if order == nil {
			s.logger.Error().
				Int64("oid", sess.ActiveBuyExtOrderID).
				Msg("active ext buy order not fount in order list")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		atomic.StoreInt64(&sess.ActiveBuyOrderID, order.ID)
		atomic.StoreInt64(&sess.ActiveBuyExtOrderID, 0)

		go s.manageOrder(ctx, sess, sess.ActiveBuyOrderID)
	} else if sess.ActiveSellExtOrderID != 0 {
		id, err := s.wsSvc.GetOrder(sess.ActiveSellExtOrderID)
		if err != nil {
			s.logger.Err(err).
				Int64("oid", sess.ActiveSellExtOrderID).
				Msg("get order")
			cancel()

			interrupt <- syscall.SIGINT
		}

		err = s.orderSvc.UpdateOrderStates(ctx, interrupt, id)
		if err != nil {
			s.logger.Err(err).
				Int64("oid", sess.ActiveSellExtOrderID).
				Msg("get order by ext id")
			cancel()

			interrupt <- syscall.SIGINT
		}

		order := s.storage.GetUserOrder(sess.ActiveSellExtOrderID)
		if order == nil {
			s.logger.Error().
				Int64("oid", sess.ActiveSellExtOrderID).
				Msg("active ext buy order not fount in order list")
			cancel()

			interrupt <- syscall.SIGINT

			return
		}

		atomic.StoreInt64(&sess.ActiveSellOrderID, order.ID)
		atomic.StoreInt64(&sess.ActiveSellExtOrderID, 0)

		go s.manageOrder(ctx, sess, sess.ActiveSellOrderID)
	}

	s.processSession(sessCtx, sess, interrupt)
	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

	cancel()
}

func (s *Spread) processSession(
	ctx context.Context,
	sess *storage.Session,
	interrupt chan os.Signal,
) {
	go func() {
		if err := s.listenNewOrders(ctx, interrupt, sess); err != nil {
			s.logger.Err(err).Msg("listen new orders")

			interrupt <- syscall.SIGSTOP
		}
	}()

	s.orderCreationDecider(ctx, sess, interrupt)
}

func (s *Spread) orderCreationDecider(ctx context.Context, sess *storage.Session, interrupt chan os.Signal) {
	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("order creation decider starting...")
	defer s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("order creation decider stopped")

	e := s.orderBook.EventBroker.Subscribe()
	defer s.orderBook.EventBroker.Unsubscribe(e)

	for {
		select {
		case <-e:
			if sess.IsDone.IsSet() {
				return
			}

			if sess.IsNeedToCreateBuyOrder.IsSet() &&
				s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.orderBook.GetSpread().Cmp(s.spreadForStartBuy) >= 0 {
				buyVolume, _, _, err := s.calculateBuyOrderVolume(sess)
				if err != nil {
					s.logger.Err(err).Msg("calc buy volume")

					interrupt <- syscall.SIGINT

					return
				}

				maxBidPrice := s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)
				maxBid := s.orderBook.GetBid(maxBidPrice)

				if maxBid == nil {
					continue
				}

				if maxBid.Amount.Cmp(buyVolume) >= 0 {
					if err := s.createBuyOrder(sess); err != nil {
						s.logger.Err(err).Msg("create buy order")

						if errors.Is(err, dictionary.ErrInsufficientFunds) {
							continue
						}

						interrupt <- syscall.SIGINT

						return
					}
				}
			}

			// need to create sell order
			if sess.IsNeedToCreateSellOrder.IsSet() &&
				s.calcBuySpread(sess.ActiveBuyOrderID).Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.calcBuySpread(sess.ActiveBuyOrderID).Cmp(s.spreadForStartSell) == 1 {
				sellVolume, _, _, err := s.calculateSellOrderVolume(sess)
				if err != nil {
					interrupt <- syscall.SIGINT

					return
				}

				minAskPrice := s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)
				minAsk := s.orderBook.GetAsk(minAskPrice)

				if minAsk == nil {
					continue
				}

				if minAsk.Amount.Cmp(sellVolume) >= 0 {
					if err := s.createSellOrder(sess); err != nil {
						s.logger.Err(err).Msg("create sell order")

						if errors.Is(err, dictionary.ErrInsufficientFunds) {
							continue
						}

						interrupt <- syscall.SIGINT

						return
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Spread) listenNewOrders(
	ctx context.Context,
	interrupt chan os.Signal,
	sess *storage.Session,
) error {
	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("listen new orders subscriber starting...")
	defer s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("listen new orders subscriber stopped")

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

			if strconv.FormatInt(atomic.LoadInt64(&sess.ActiveBuyExtOrderID), 10) != rid.ID &&
				strconv.FormatInt(atomic.LoadInt64(&sess.ActiveSellExtOrderID), 10) != rid.ID {
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

			if strconv.FormatInt(atomic.LoadInt64(&sess.ActiveBuyExtOrderID), 10) == rid.ID {
				atomic.StoreInt64(&sess.ActiveBuyOrderID, co.OrderID)
				atomic.StoreInt64(&sess.ActiveBuyExtOrderID, 0)
				s.storage.AddBuyOrder(s.pair, sess.ID, co.OrderID)
			}

			if strconv.FormatInt(atomic.LoadInt64(&sess.ActiveSellExtOrderID), 10) == rid.ID {
				atomic.StoreInt64(&sess.ActiveSellOrderID, co.OrderID)
				atomic.StoreInt64(&sess.ActiveSellExtOrderID, 0)
				s.storage.AddSellOrder(s.pair, sess.ID, co.OrderID)
			}

			go s.manageOrder(ctx, sess, co.OrderID)

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
		id, parseErr := strconv.ParseInt(er.ID, 10, 0)
		if parseErr != nil {
			s.logger.Err(parseErr).Str("val", er.ID).Msg("parse string as int error")

			return false, err
		}

		// probably prev order executed on max available amount
		if er.Error.Code == response.AmountTooSmallCode &&
			(sess.GetPrevBuyOrderID() != 0 || sess.GetPrevSellOrderID() != 0) {
			if id == atomic.LoadInt64(&sess.ActiveBuyExtOrderID) {
				s.logger.Warn().
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", sess.GetPrevBuyOrderID()).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("consider prev buy order as executed, allow to place sell order")

				s.setBuyOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevBuyOrderID()))
			} else if id == atomic.LoadInt64(&sess.ActiveSellExtOrderID) {
				s.logger.Warn().
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", sess.GetPrevSellOrderID()).
					Int64("oid", sess.GetActiveSellOrderID()).
					Msg("consider prev sell order as executed, allow to place buy order")

				s.setSellOrderExecutedFlags(sess, s.storage.GetUserOrder(sess.GetPrevSellOrderID()))
			}

			return true, nil
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

	amount, total, price, err := s.calculateBuyOrderVolume(sess)
	if err != nil {
		s.logger.Err(err).Msg("calculate buy order volume")

		return err
	}

	s.logger.
		Warn().
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

	extID, err := s.wsSvc.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.BuyBase,
	)
	if err != nil {
		s.logger.Err(err).Msg("create buy order")

		return err
	}

	atomic.StoreInt64(&sess.ActiveBuyExtOrderID, extID)

	return nil
}

func (s *Spread) createSellOrder(sess *storage.Session) error {
	sess.IsNeedToCreateSellOrder.UnSet()

	amount, total, price, err := s.calculateSellOrderVolume(sess)
	if err != nil {
		s.logger.Err(err).Msg("calculate sell order volume")

		return err
	}

	s.logger.
		Warn().
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

	extID, err := s.wsSvc.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.SellBase,
	)
	if err != nil {
		s.logger.Err(err).Msg("create sell order")

		return err
	}

	atomic.StoreInt64(&sess.ActiveSellExtOrderID, extID)

	return nil
}

func (s *Spread) manageOrder(ctx context.Context, sess *storage.Session, orderID int64) {
	s.logger.Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("start order manager process")

	e := s.orderBook.EventBroker.Subscribe()

	defer s.orderBook.EventBroker.Unsubscribe(e)
	defer s.logger.Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("stop order manager process")

	for {
		select {
		case <-e:
			// order sent, wait creation
			order := s.storage.GetUserOrder(orderID)
			if order == nil {
				s.logger.Warn().Int64("oid", orderID).Msg("order not found")

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
						Int("state", order.State).
						Int64("id", orderID).
						Msg("buy order reached final state")

					if order.State == dictionary.StateDone {
						s.setBuyOrderExecutedFlags(sess, order)

						return
					}

					if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&sess.PrevBuyOrderID, orderID)
						atomic.StoreInt64(&sess.ActiveBuyOrderID, 0)
						sess.IsNeedToCreateBuyOrder.Set()

						return
					}
				} else if order.TradeIntent == dictionary.SellBase {
					s.logger.Warn().
						Int("state", order.State).
						Int64("id", orderID).
						Msg("sell order reached final state")

					if order.State == dictionary.StateDone {
						s.setSellOrderExecutedFlags(sess, order)
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&sess.ActiveSellOrderID, 0)
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
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel buy order")

					err := s.cancelOrder(orderID)
					if err != nil {
						if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.orderBook.EventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("cancel buy order")
					}

					atomic.StoreInt64(&sess.PrevBuyOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveBuyOrderID, 0)
					sess.IsNeedToCreateBuyOrder.Set()

					return
				}

				// move buy order
				previousPossiblePrice := big.NewFloat(0).Sub(order.LimitPrice, s.priceStep)

				prevPriceExists := s.orderBook.GetBid(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
				nextPriceExists := s.orderBook.GetMaxBidPrice().Cmp(order.LimitPrice) == 1

				nextBidPrice := s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)

				nextBid := s.orderBook.GetBid(nextBidPrice)

				if (nextPriceExists) || !prevPriceExists {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Str("order price", order.LimitPrice.Text('f', s.pair.PriceScale)).
						Str("max bid price", nextBidPrice).
						Bool("next bid price exists", nextPriceExists).
						Str("next bid volume", nextBid.Amount.String()).
						Str("volume", order.OrderedVolume.String()).
						Str("prev possible bid price", previousPossiblePrice.Text('f', -1)).
						Bool("prev possible bid price exists", prevPriceExists).
						Msg("time to move buy order")

					err := s.cancelOrder(orderID)
					if err != nil {
						if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.orderBook.EventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order for move")
					}

					atomic.StoreInt64(&sess.PrevBuyOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveBuyOrderID, 0)
					sess.IsNeedToCreateBuyOrder.Set()

					s.orderBook.EventBroker.Publish(0) // don't wait change order book

					return
				}
			}

			if order.TradeIntent == dictionary.SellBase {
				spread := s.calcBuySpread(sess.GetActiveBuyOrderID())

				// cancel sell order
				if spread.Cmp(s.spreadForStopBuyTrade) == -1 {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel sell order")

					err := s.cancelOrder(orderID)
					if err != nil {
						if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.orderBook.EventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("can't cancel order")
					}

					atomic.StoreInt64(&sess.PrevSellOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveSellOrderID, 0)
					sess.IsNeedToCreateSellOrder.Set()

					return
				}

				// move sell order
				previousPossiblePrice := big.NewFloat(0).Add(order.LimitPrice, s.priceStep)

				prevPriceExists := s.orderBook.GetAsk(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
				nextPriceExists := s.orderBook.GetMinAskPrice().Cmp(order.LimitPrice) == -1

				if nextPriceExists || !prevPriceExists {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("order price", order.LimitPrice.Text('f', s.pair.PriceScale)).
						Str("min ask price", s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)).
						Str("spread", spread.String()).
						Bool("next ask price exists", nextPriceExists).
						Str("prev possible ask price", previousPossiblePrice.Text('f', -1)).
						Bool("prev possible ask price exists", prevPriceExists).
						Msg("time to move sell order")

					err := s.cancelOrder(orderID)
					if err != nil {
						if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.orderBook.EventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
					}

					atomic.StoreInt64(&sess.PrevSellOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveSellOrderID, 0)
					sess.IsNeedToCreateSellOrder.Set()

					s.orderBook.EventBroker.Publish(0)

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

func (s *Spread) setBuyOrderExecutedFlags(sess *storage.Session, order *storage.Order) {
	atomic.AddInt64(&s.orderBook.CompletedBuyOrders, 1)
	atomic.StoreInt64(&sess.PrevBuyOrderID, 0)
	atomic.StoreInt64(&sess.ActiveBuyOrderID, order.ID)
	atomic.StoreInt64(&sess.ActiveSellOrderID, 0)
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
	atomic.StoreInt64(&sess.PrevSellOrderID, 0)
	atomic.StoreInt64(&sess.ActiveSellOrderID, 0)

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

func (s *Spread) calculateBuyOrderVolume(sess *storage.Session) (
	amount *big.Float,
	total *big.Float,
	price *big.Float,
	err error,
) {
	price = big.NewFloat(0).Add(s.orderBook.GetMaxBidPrice(), s.priceStep)
	total = big.NewFloat(0)
	amount = big.NewFloat(0)

	if sess.GetPrevBuyOrderID() == 0 {
		total, err = s.getTotalBuyVolume()
		if err != nil {
			s.logger.Err(err).Msg("parse string as float")

			return nil, nil, nil, err
		}

		amount.Quo(total, price)

		return amount, total, price, nil
	}

	prevBuyOrder := s.storage.GetUserOrder(sess.GetPrevBuyOrderID())

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

func (s *Spread) calculateSellOrderVolume(sess *storage.Session) (
	amount *big.Float,
	total *big.Float,
	price *big.Float,
	err error,
) {
	price = big.NewFloat(0).Sub(s.orderBook.GetMinAskPrice(), s.priceStep)
	total = big.NewFloat(0)
	buyOrder := s.storage.GetUserOrder(sess.GetActiveBuyOrderID())

	var ok bool

	if sess.GetPrevSellOrderID() == 0 {
		amount, ok = s.storage.GetTotalBuyVolume(s.pair, sess.ID)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Int64("oid", buyOrder.ID).
				Int64("prev oid", sess.GetPrevSellOrderID()).
				Msg("parse string as float")

			return nil, nil, nil, dictionary.ErrParseFloat
		}
	} else {
		prevSellOrder := s.storage.GetUserOrder(sess.GetPrevSellOrderID())
		amount = prevSellOrder.OrderedVolume

		if prevSellOrder.TotalSellVolume.Cmp(dictionary.ZeroBigFloat) != 0 {
			newAmount := amount.Sub(amount, prevSellOrder.TotalSellVolume)
			amount = newAmount
		}
	}

	total.Mul(amount, price)

	return amount, total, price, nil
}
