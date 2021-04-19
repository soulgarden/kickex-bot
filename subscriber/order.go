package subscriber

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Order struct {
	cfg                    *conf.Bot
	pair                   *response.Pair
	orderBook              *storage.Book
	storage                *storage.Storage
	eventBroker            *Broker
	priceStep              *big.Float
	spreadForStartBuy      *big.Float
	spreadForStartSell     *big.Float
	spreadForStopBuyTrade  *big.Float
	spreadForStopSellTrade *big.Float
	totalBuyVolume         *big.Float
	logger                 *zerolog.Logger
}

func NewOrder(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *Broker,
	pair *response.Pair,
	logger *zerolog.Logger,
) (*Order, error) {
	zeroStep := big.NewFloat(0).Text('f', pair.PriceScale)
	priceStepStr := zeroStep[0:pair.PriceScale+1] + "1"

	priceStep, ok := big.NewFloat(0).SetPrec(uint(pair.PriceScale)).SetString(priceStepStr)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartTrade, ok := big.NewFloat(0).SetString(cfg.SpreadForStartBuy)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStartSell, ok := big.NewFloat(0).SetString(cfg.SpreadForStartSell)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopBuyTrade, ok := big.NewFloat(0).SetString(cfg.SpreadForStopBuyTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	spreadForStopSellTrade, ok := big.NewFloat(0).SetString(cfg.SpreadForStopSellTrade)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	pairMinVolume, ok := big.NewFloat(0).SetString(pair.MinVolume)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	totalBuyVolumeScale, ok := big.NewFloat(0).SetString(cfg.TotalBuyVolumeScale)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", priceStepStr).Msg("parse string as float error")

		return nil, dictionary.ErrParseFloat
	}

	totalBuyVolume := big.NewFloat(0).Mul(pairMinVolume, totalBuyVolumeScale)

	return &Order{
		cfg:                    cfg,
		pair:                   pair,
		storage:                storage,
		eventBroker:            eventBroker,
		priceStep:              priceStep,
		spreadForStartBuy:      spreadForStartTrade,
		spreadForStartSell:     spreadForStartSell,
		spreadForStopBuyTrade:  spreadForStopBuyTrade,
		spreadForStopSellTrade: spreadForStopSellTrade,
		totalBuyVolume:         totalBuyVolume,
		orderBook:              storage.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency],
		logger:                 logger,
	}, nil
}

func (s *Order) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan os.Signal) {
	defer func() {
		wg.Done()
	}()

	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("order manager starting...")

	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)

	if err != nil {
		s.logger.Err(err).Msg("connection error")

		interrupt <- syscall.SIGSTOP

		return
	}

	defer cli.Close()

	err = cli.Auth()
	if err != nil {
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case <-ctx.Done():
			s.cleanUpActiveOrders(cli)
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders")

			return
		case <-time.After(time.Millisecond):
			sessCtx, cancel := context.WithCancel(ctx)
			sess := s.orderBook.NewSession()

			s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("start session")

			s.processSession(sessCtx, cli, sess, interrupt)
			s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

			cancel()
		}
	}
}

func (s *Order) processSession(ctx context.Context, cli *client.Client, sess *storage.Session, interrupt chan os.Signal) {
	go func() {
		if err := s.listenNewOrders(ctx, interrupt, cli, sess); err != nil {
			interrupt <- syscall.SIGSTOP
		}
	}()

	s.orderCreationDecider(ctx, cli, sess)
}

func (s *Order) orderCreationDecider(ctx context.Context, cli *client.Client, sess *storage.Session) {
	e := s.eventBroker.Subscribe()
	defer s.eventBroker.Unsubscribe(e)

	for {
		select {
		case <-e:
			if sess.IsDone.IsSet() {
				return
			}

			buyOrderDecision := s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.orderBook.GetSpread().Cmp(s.spreadForStartBuy) == 1 &&
				atomic.LoadInt64(&sess.ActiveSellOrderID) == 0 &&
				atomic.LoadInt64(&sess.ActiveBuyOrderID) == 0

			// need to create buy order
			if buyOrderDecision {
				s.createBuyOrder(cli, sess)
			}

			sellOrderDecision := atomic.LoadInt64(&sess.ActiveBuyOrderID) > 1 &&
				atomic.LoadInt64(&sess.ActiveSellOrderID) == 1 &&
				s.calcBuySpread(sess.ActiveBuyOrderID).Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.calcBuySpread(sess.ActiveBuyOrderID).Cmp(s.spreadForStartSell) == 1

			// need to create sell order
			if sellOrderDecision {
				s.createSellOrder(cli, sess)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Order) listenNewOrders(
	ctx context.Context,
	interrupt chan os.Signal,
	cli *client.Client,
	sess *storage.Session,
) error {
	attempts := 0

	var err error

	var skip bool

	for {
		select {
		case msg, ok := <-cli.ReadCh:
			if !ok {
				return nil
			}

			s.logger.Warn().
				Bytes("payload", msg).
				Msg("got message")

			skip, attempts, err = s.checkListenOrderErrors(msg, attempts, sess)
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

			go s.manageOrder(ctx, interrupt, sess, co.OrderID)

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Order) checkListenOrderErrors(msg []byte, attempts int, sess *storage.Session) (bool, int, error) {
	er := &response.Error{}

	err := json.Unmarshal(msg, er)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return false, attempts, err
	}

	if er.Error != nil {
		attempts++

		id, parseErr := strconv.ParseInt(er.ID, 10, 0)
		if parseErr != nil {
			s.logger.Err(parseErr).Str("val", er.ID).Msg("parse string as int error")

			return false, attempts, err
		}

		// probably prev order executed on max available amount
		if er.Error.Reason == response.AmountTooSmall &&
			(atomic.LoadInt64(&sess.PrevBuyOrderID) != 0 || atomic.LoadInt64(&sess.PrevSellOrderID) != 0) {
			if id == atomic.LoadInt64(&sess.ActiveBuyOrderID) {
				s.logger.Warn().
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", atomic.LoadInt64(&sess.PrevBuyOrderID)).
					Int64("oid", atomic.LoadInt64(&sess.ActiveBuyOrderID)).
					Msg("consider prev buy order as executed, allow to place sell order")

				s.setBuyOrderExecutedFlags(sess, atomic.LoadInt64(&sess.PrevBuyOrderID))
			} else if id == atomic.LoadInt64(&sess.ActiveSellOrderID) {
				s.logger.Warn().
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", atomic.LoadInt64(&sess.PrevSellOrderID)).
					Int64("oid", atomic.LoadInt64(&sess.ActiveSellOrderID)).
					Msg("consider prev sell order as executed, allow to place buy order")

				s.setSellOrderExecutedFlags(sess)
			}

			return true, attempts, nil
		}

		if er.Error.Code == response.InsufficientFundsCode {
			if id == atomic.LoadInt64(&sess.ActiveSellOrderID) && attempts < 10 {
				atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order

				return true, attempts, nil
			} else if id == atomic.LoadInt64(&sess.ActiveBuyOrderID) && attempts < 10 {
				atomic.StoreInt64(&sess.ActiveBuyOrderID, 0) // need to create new buy order

				return true, attempts, nil
			}
		}

		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return false, attempts, nil
	}

	return false, 0, nil
}

func (s *Order) createBuyOrder(cli *client.Client, sess *storage.Session) {
	atomic.StoreInt64(&sess.ActiveBuyOrderID, 1)

	price := big.NewFloat(0).Add(s.orderBook.GetMaxBidPrice(), s.priceStep)
	total := big.NewFloat(0)
	amount := big.NewFloat(0)

	if atomic.LoadInt64(&sess.PrevBuyOrderID) == 0 {
		total = s.totalBuyVolume
		amount.Quo(total, price)
	} else {
		prevBuyOrder := s.storage.GetUserOrder(atomic.LoadInt64(&sess.PrevBuyOrderID))
		orderedAmount, ok := big.NewFloat(0).SetString(prevBuyOrder.OrderedVolume)
		if !ok {
			s.logger.Fatal().
				Str("val", prevBuyOrder.OrderedVolume).
				Msg("parse string as float")
		}

		orderedLimitPrice, ok := big.NewFloat(0).SetString(prevBuyOrder.LimitPrice)
		if !ok {
			s.logger.Fatal().
				Str("val", prevBuyOrder.LimitPrice).
				Msg("parse string as float")
		}

		orderedTotal := big.NewFloat(0).Mul(orderedAmount, orderedLimitPrice)

		if prevBuyOrder.TotalBuyVolume != "" {
			soldAmount, ok := big.NewFloat(0).SetString(prevBuyOrder.TotalBuyVolume)
			if !ok {
				s.logger.Fatal().
					Str("val", prevBuyOrder.TotalBuyVolume).
					Msg("parse string as float")
			}

			soldTotal := big.NewFloat(0).Mul(soldAmount, orderedLimitPrice)

			total.Sub(orderedTotal, soldTotal)
			amount.Quo(total, price)
		} else {
			amount = amount.Quo(orderedTotal, price)
			total.Mul(amount, price)
		}
	}

	s.logger.
		Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("prev order id", atomic.LoadInt64(&sess.PrevBuyOrderID)).
		Str("spread", s.orderBook.GetSpread().Text('f', s.pair.PriceScale)).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Msg("time to place buy order")

	var err error

	// todo subscribe to balance updates for ensure that assets is enough
	extID, err := cli.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.BuyBase,
	)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("create buy order")
	}

	atomic.StoreInt64(&sess.ActiveBuyOrderID, extID)
}

func (s *Order) createSellOrder(cli *client.Client, sess *storage.Session) {
	price := big.NewFloat(0)
	price.Sub(s.orderBook.GetMinAskPrice(), s.priceStep)

	buyOrder := s.storage.GetUserOrder(atomic.LoadInt64(&sess.ActiveBuyOrderID))

	var ok bool

	amount := big.NewFloat(0)

	if atomic.LoadInt64(&sess.PrevSellOrderID) == 0 {
		amount, ok = s.storage.GetTotalBuyVolume(s.pair, sess.ID)
		if !ok {
			s.logger.Fatal().
				Int64("oid", buyOrder.ID).
				Int64("prev oid", atomic.LoadInt64(&sess.PrevSellOrderID)).
				Msg("parse string as float")
		}
	} else {
		prevSellOrder := s.storage.GetUserOrder(atomic.LoadInt64(&sess.PrevSellOrderID))
		amount, ok = amount.SetString(prevSellOrder.OrderedVolume)
		if !ok {
			s.logger.Fatal().
				Int64("oid", buyOrder.ID).
				Int64("prev oid", atomic.LoadInt64(&sess.PrevSellOrderID)).
				Str("val", prevSellOrder.OrderedVolume).
				Msg("parse string as float")
		}

		if prevSellOrder.TotalSellVolume != "" {
			soldAmount, ok := big.NewFloat(0).SetString(prevSellOrder.TotalSellVolume)
			if !ok {
				s.logger.Fatal().
					Str("val", prevSellOrder.TotalSellVolume).
					Msg("parse string as float")
			}

			newAmount := amount.Sub(amount, soldAmount)
			amount = newAmount
		}
	}

	total := big.NewFloat(0)

	total.Mul(amount, price)

	spread := s.calcBuySpread(atomic.LoadInt64(&sess.ActiveBuyOrderID))

	orderVolume := amount.Text('f', s.pair.QuantityScale)
	orderPrice := price.Text('f', s.pair.PriceScale)

	s.logger.
		Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("prev order id", atomic.LoadInt64(&sess.PrevSellOrderID)).
		Str("price", orderPrice).
		Str("amount", orderVolume).
		Str("total", total.String()).
		Str("spread", spread.String()).
		Msg("time to place sell order")

	var err error

	// todo subscribe to balance updates for ensure that assets is enough
	extID, err := cli.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.SellBase,
	)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("create sell order")
	}

	atomic.StoreInt64(&sess.ActiveSellOrderID, extID)
}

func (s *Order) manageOrder(ctx context.Context, interrupt chan os.Signal, sess *storage.Session, orderID int64) {
	s.logger.Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("oid", orderID).
		Msg("start order manager process")

	e := s.eventBroker.Subscribe()

	defer func() {
		s.logger.Warn().
			Str("pair", s.pair.GetPairName()).
			Int64("oid", orderID).
			Msg("stop order manager process")

		s.eventBroker.Unsubscribe(e)
	}()

	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")

		interrupt <- syscall.SIGSTOP

		return
	}

	err = cli.Auth()
	if err != nil {
		interrupt <- syscall.SIGSTOP

		return
	}

	defer cli.Close()

	for {
		select {
		case <-e:
			// order sent, wait creation
			order := s.storage.GetUserOrder(orderID)
			if order == nil {
				continue
			}

			if order.TradeIntent == dictionary.BuyBase {
				atomic.StoreInt64(&sess.ActiveBuyOrderID, orderID)
				s.storage.AddBuyOrder(s.pair, sess.ID, orderID)
			} else if order.TradeIntent == dictionary.SellBase {
				atomic.StoreInt64(&sess.ActiveSellOrderID, orderID)
				s.storage.AddSellOrder(s.pair, sess.ID, orderID)
			}

			if order.State < dictionary.StateActive {
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
						s.setBuyOrderExecutedFlags(sess, atomic.LoadInt64(&sess.ActiveBuyOrderID))
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&sess.ActiveBuyOrderID, 0) // need to create new buy order
						atomic.StoreInt64(&sess.PrevBuyOrderID, orderID)
					}
				} else if order.TradeIntent == dictionary.SellBase {
					s.logger.Warn().
						Int("state", order.State).
						Int64("id", orderID).
						Msg("sell order reached final state")

					if order.State == dictionary.StateDone {
						s.setSellOrderExecutedFlags(sess)
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order
					}
				}

				return
			}

			if order.TradeIntent == dictionary.BuyBase {
				spread := s.calcBuySpread(atomic.LoadInt64(&sess.ActiveBuyOrderID))
				// cancel buy order
				if spread.Cmp(s.spreadForStopBuyTrade) == -1 && s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel buy order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("cancel buy order")
					}

					atomic.StoreInt64(&sess.ActiveBuyOrderID, 0) // need to create new buy order
					atomic.StoreInt64(&sess.PrevBuyOrderID, orderID)

					return
				}

				// move buy order
				buyOrderPrice, ok := big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Fatal().Str("val", order.LimitPrice).Msg("parse float as string")
				}

				previousPossiblePrice := big.NewFloat(0).Sub(buyOrderPrice, s.priceStep)

				prevPriceExists := s.orderBook.GetBid(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
				nextPriceExists := s.orderBook.GetMaxBidPrice().Cmp(buyOrderPrice) == 1

				if nextPriceExists || !prevPriceExists {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Str("order price", buyOrderPrice.Text('f', s.pair.PriceScale)).
						Str("max bid price", s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)).
						Bool("larger bid price exists", nextPriceExists).
						Str("prev possible bid price", previousPossiblePrice.Text('f', -1)).
						Bool("prev possible bid price exists", prevPriceExists).
						Msg("time to move buy order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order for move")
					}

					atomic.StoreInt64(&sess.ActiveBuyOrderID, 0) // need to create new buy order
					atomic.StoreInt64(&sess.PrevBuyOrderID, orderID)

					s.eventBroker.Publish(0) // don't wait change order book

					return
				}
			}

			if order.TradeIntent == dictionary.SellBase {
				spread := s.calcBuySpread(atomic.LoadInt64(&sess.ActiveBuyOrderID))

				// cancel sell order
				if spread.Cmp(s.spreadForStopBuyTrade) == -1 {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel sell order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
					}

					atomic.StoreInt64(&sess.PrevSellOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order

					return
				}

				// move sell order
				sellOrderPrice, ok := big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Fatal().Str("price", order.LimitPrice).Msg("parse limit price")
				}

				previousPossiblePrice := big.NewFloat(0).Add(sellOrderPrice, s.priceStep)

				prevPriceExists := s.orderBook.GetAsk(previousPossiblePrice.Text('f', s.pair.PriceScale)) != nil
				nextPriceExists := s.orderBook.GetMinAskPrice().Cmp(sellOrderPrice) == -1

				if nextPriceExists || !prevPriceExists {
					s.logger.Warn().
						Str("pair", s.pair.GetPairName()).
						Int64("oid", orderID).
						Str("order price", sellOrderPrice.Text('f', s.pair.PriceScale)).
						Str("min ask price", s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)).
						Str("spread", spread.String()).
						Bool("larger ask price exists", nextPriceExists).
						Str("prev possible ask price", previousPossiblePrice.Text('f', -1)).
						Bool("prev possible ask price exists", prevPriceExists).
						Msg("time to move sell order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
					}

					atomic.StoreInt64(&sess.PrevSellOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order

					s.eventBroker.Publish(0) // don't wait change order book

					return
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

func (s *Order) calcBuySpread(activeBuyOrderID int64) *big.Float {
	if o := s.storage.GetUserOrder(activeBuyOrderID); o == nil {
		return dictionary.ZeroBigFloat
	}

	buyBidPrice, ok := big.NewFloat(0).SetString(s.storage.GetUserOrder(activeBuyOrderID).LimitPrice)
	if !ok {
		s.logger.Fatal().
			Str("val", s.storage.GetUserOrder(activeBuyOrderID).LimitPrice).
			Msg("parse string as float")
	}

	// 100 - (x * 100 / y)
	return big.NewFloat(0).Sub(
		dictionary.MaxPercentFloat,
		big.NewFloat(0).Quo(
			big.NewFloat(0).Mul(buyBidPrice, dictionary.MaxPercentFloat),
			s.orderBook.GetMinAskPrice()),
	)
}

func (s *Order) cancelOrder(cli *client.Client, orderID int64) error {
	err := cli.CancelOrder(orderID)
	if err != nil {
		s.logger.Fatal().Int64("oid", orderID).Msg("cancel order")
	}

	msg, ok := <-cli.ReadCh
	if !ok {
		return nil
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
			s.logger.Error().Bytes("payload", msg).Msg("can't cancel order")

			return errors.New(er.Error.Reason)
		}
	}

	return nil
}

func (s *Order) setBuyOrderExecutedFlags(sess *storage.Session, activeBuyOrderID int64) {
	atomic.AddInt64(&s.orderBook.CompletedBuyOrders, 1)
	atomic.StoreInt64(&sess.ActiveBuyOrderID, activeBuyOrderID)
	atomic.StoreInt64(&sess.PrevBuyOrderID, 0)
	atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order
}

func (s *Order) setSellOrderExecutedFlags(sess *storage.Session) {
	atomic.AddInt64(&s.orderBook.CompletedSellOrders, 1)
	// atomic.StoreInt64(&sess.ActiveBuyOrderID, 0) // need to create new buy order
	atomic.StoreInt64(&sess.PrevSellOrderID, 0)
	atomic.StoreInt64(&sess.ActiveSellOrderID, 0)

	sess.IsDone.Set()
}

func (s *Order) cleanUpActiveOrders(cli *client.Client) {
	for _, sess := range s.orderBook.Session {
		// cleanup orders
		if atomic.LoadInt64(&sess.ActiveBuyOrderID) > 1 {
			if o := s.storage.GetUserOrder(atomic.LoadInt64(&sess.ActiveBuyOrderID)); o != nil && o.State < dictionary.StateDone {
				err := s.cancelOrder(cli, atomic.LoadInt64(&sess.ActiveBuyOrderID))

				s.logger.Err(err).Int64("oid", atomic.LoadInt64(&sess.ActiveBuyOrderID)).Msg("try to cancel active buy order")
			}
		}

		if atomic.LoadInt64(&sess.ActiveSellOrderID) > 1 {
			if o := s.storage.GetUserOrder(atomic.LoadInt64(&sess.ActiveSellOrderID)); o != nil && o.State < dictionary.StateDone {
				err := s.cancelOrder(cli, atomic.LoadInt64(&sess.ActiveSellOrderID))
				s.logger.Err(err).Int64("oid", atomic.LoadInt64(&sess.ActiveSellOrderID)).Msg("try to cancel active sell order")
			}
		}
	}
}
