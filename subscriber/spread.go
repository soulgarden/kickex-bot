package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Spread struct {
	cfg         *conf.Bot
	pair        *response.Pair
	orderBook   *storage.Book
	storage     *storage.Storage
	eventBroker *Broker
	conversion  *service.Conversion
	tgSvc       *service.Telegram

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
	eventBroker *Broker,
	conversion *service.Conversion,
	tgSvc *service.Telegram,
	pair *response.Pair,
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
		eventBroker:            eventBroker,
		conversion:             conversion,
		tgSvc:                  tgSvc,
		priceStep:              priceStep,
		spreadForStartBuy:      spreadForStartTrade,
		spreadForStartSell:     spreadForStartSell,
		spreadForStopBuyTrade:  spreadForStopBuyTrade,
		spreadForStopSellTrade: spreadForStopSellTrade,
		orderBook:              storage.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency],
		logger:                 logger,
	}, nil
}

func (s *Spread) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan os.Signal) {
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
					s.processOldSession(ctx, cli, sess, interrupt)
				}
			}

		case <-ctx.Done():
			s.cleanUpActiveOrders()
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders")

			close(oldSessionChan)

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

func (s *Spread) processOldSession(
	ctx context.Context,
	cli *client.Client,
	sess *storage.Session,
	interrupt chan os.Signal,
) {
	sessCtx, cancel := context.WithCancel(ctx)

	s.logger.Warn().Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("start old session")

	if sess.ActiveSellOrderID != 0 {
		order := s.storage.GetUserOrder(sess.ActiveSellOrderID)
		if order == nil {
			s.logger.Error().
				Int64("oid", sess.ActiveSellOrderID).
				Msg("active sell order not fount in order list, skip broken session")
			cancel()
		}

		go s.manageOrder(ctx, interrupt, sess, sess.ActiveSellOrderID)
	} else if sess.ActiveBuyOrderID != 0 {
		order := s.storage.GetUserOrder(sess.ActiveBuyOrderID)
		if order == nil {
			s.logger.Error().
				Int64("oid", sess.ActiveBuyOrderID).
				Msg("active buy order not fount in order list, skip broken session")
			cancel()
		}

		go s.manageOrder(ctx, interrupt, sess, sess.ActiveBuyOrderID)
	}

	s.processSession(sessCtx, cli, sess, interrupt)
	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

	cancel()
}

func (s *Spread) processSession(
	ctx context.Context,
	cli *client.Client,
	sess *storage.Session,
	interrupt chan os.Signal,
) {
	go func() {
		if err := s.listenNewOrders(ctx, interrupt, cli, sess); err != nil {
			interrupt <- syscall.SIGSTOP
		}
	}()

	s.orderCreationDecider(ctx, cli, sess, interrupt)
}

func (s *Spread) orderCreationDecider(ctx context.Context, cli *client.Client, sess *storage.Session, interrupt chan os.Signal) {
	e := s.eventBroker.Subscribe()
	defer s.eventBroker.Unsubscribe(e)

	for {
		select {
		case <-e:
			if sess.IsDone.IsSet() {
				return
			}

			// need to create buy order
			if atomic.LoadInt64(&sess.ActiveBuyOrderID) == 0 &&
				atomic.LoadInt64(&sess.ActiveSellOrderID) == 0 &&
				s.orderBook.GetSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.orderBook.GetSpread().Cmp(s.spreadForStartBuy) >= 0 {
				buyVolume, _, _, err := s.calculateBuyOrderVolume(sess)
				if err != nil {
					interrupt <- syscall.SIGINT

					return
				}

				maxBidPrice := s.orderBook.GetMaxBidPrice().Text('f', s.pair.PriceScale)
				maxBid := s.orderBook.GetBid(maxBidPrice)

				if maxBid.Amount.Cmp(buyVolume) >= 0 {
					if err := s.createBuyOrder(cli, sess); err != nil {
						interrupt <- syscall.SIGINT

						return
					}
				}
			}

			// need to create sell order
			if atomic.LoadInt64(&sess.ActiveBuyOrderID) > 1 &&
				atomic.LoadInt64(&sess.ActiveSellOrderID) == 1 &&
				s.calcBuySpread(sess.ActiveBuyOrderID).Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.calcBuySpread(sess.ActiveBuyOrderID).Cmp(s.spreadForStartSell) == 1 {
				sellVolume, _, _, err := s.calculateSellOrderVolume(sess)
				if err != nil {
					interrupt <- syscall.SIGINT

					return
				}

				minAskPrice := s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale)
				minAsk := s.orderBook.GetAsk(minAskPrice)

				if minAsk.Amount.Cmp(sellVolume) >= 0 {
					if err := s.createSellOrder(cli, sess); err != nil {
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

func (s *Spread) checkListenOrderErrors(msg []byte, attempts int, sess *storage.Session) (bool, int, error) {
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
		if er.Error.Code == response.AmountTooSmallCode &&
			(atomic.LoadInt64(&sess.PrevBuyOrderID) != 0 || atomic.LoadInt64(&sess.PrevSellOrderID) != 0) {
			if id == atomic.LoadInt64(&sess.ActiveBuyOrderID) {
				s.logger.Warn().
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", atomic.LoadInt64(&sess.PrevBuyOrderID)).
					Int64("oid", atomic.LoadInt64(&sess.ActiveBuyOrderID)).
					Msg("consider prev buy order as executed, allow to place sell order")

				s.setBuyOrderExecutedFlags(sess, s.storage.UserOrders[id])
			} else if id == atomic.LoadInt64(&sess.ActiveSellOrderID) {
				s.logger.Warn().
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", atomic.LoadInt64(&sess.PrevSellOrderID)).
					Int64("oid", atomic.LoadInt64(&sess.ActiveSellOrderID)).
					Msg("consider prev sell order as executed, allow to place buy order")

				s.setSellOrderExecutedFlags(sess, s.storage.UserOrders[id])
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

		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)

		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return false, attempts, err
	}

	return false, 0, nil
}

func (s *Spread) createBuyOrder(cli *client.Client, sess *storage.Session) error {
	atomic.StoreInt64(&sess.ActiveBuyOrderID, 1)

	var err error

	amount, total, price, err := s.calculateBuyOrderVolume(sess)
	if err != nil {
		s.logger.Err(err).Msg("calculate buy order volume")

		return err
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

	// todo subscribe to balance updates for ensure that assets is enough
	extID, err := cli.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.BuyBase,
	)
	if err != nil {
		s.logger.Err(err).Msg("create buy order")

		return err
	}

	atomic.StoreInt64(&sess.ActiveBuyOrderID, extID)

	return nil
}

func (s *Spread) createSellOrder(cli *client.Client, sess *storage.Session) error {
	amount, total, price, err := s.calculateSellOrderVolume(sess)
	if err != nil {
		s.logger.Err(err).Msg("calculate sell order volume")

		return err
	}

	s.logger.
		Warn().
		Str("pair", s.pair.GetPairName()).
		Int64("prev order id", atomic.LoadInt64(&sess.PrevSellOrderID)).
		Str("price", price.Text('f', s.pair.PriceScale)).
		Str("amount", amount.Text('f', s.pair.QuantityScale)).
		Str("total", total.String()).
		Str("spread", s.calcBuySpread(atomic.LoadInt64(&sess.ActiveBuyOrderID)).String()).
		Msg("time to place sell order")

	extID, err := cli.CreateOrder(
		s.pair.GetPairName(),
		amount.Text('f', s.pair.QuantityScale),
		price.Text('f', s.pair.PriceScale),
		dictionary.SellBase,
	)
	if err != nil {
		s.logger.Err(err).Msg("create sell order")

		return err
	}

	atomic.StoreInt64(&sess.ActiveSellOrderID, extID)

	return nil
}

func (s *Spread) manageOrder(ctx context.Context, interrupt chan os.Signal, sess *storage.Session, orderID int64) {
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
				s.logger.Warn().Int64("oid", orderID).Msg("order not found")

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
						s.setSellOrderExecutedFlags(sess, order)
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
						if err.Error() == response.DoneOrder {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.eventBroker.Publish(0)

							continue
						}

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

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						if err.Error() == response.DoneOrder {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.eventBroker.Publish(0)

							continue
						}

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
						if err.Error() == response.DoneOrder {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.eventBroker.Publish(0)

							continue
						}

						s.logger.Fatal().
							Str("pair", s.pair.GetPairName()).
							Int64("oid", orderID).
							Msg("can't cancel order")
					}

					atomic.StoreInt64(&sess.PrevSellOrderID, orderID)
					atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order

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

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						if err.Error() == response.DoneOrder {
							s.logger.Warn().
								Str("pair", s.pair.GetPairName()).
								Int64("oid", orderID).
								Msg("expected cancelled state, but got done")

							s.eventBroker.Publish(0)

							continue
						}

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

func (s *Spread) cancelOrder(cli *client.Client, orderID int64) error {
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
			err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
			s.logger.Err(err).Bytes("response", msg).Msg("can't cancel order")

			return err
		}
	}

	return nil
}

func (s *Spread) setBuyOrderExecutedFlags(sess *storage.Session, order *storage.Order) {
	atomic.AddInt64(&s.orderBook.CompletedBuyOrders, 1)
	atomic.StoreInt64(&sess.ActiveBuyOrderID, order.ID)
	atomic.StoreInt64(&sess.PrevBuyOrderID, 0)
	atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order

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

func (s *Spread) setSellOrderExecutedFlags(sess *storage.Session, order *storage.Order) {
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
	interrupt := make(chan os.Signal, 1)
	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)

	if err != nil {
		s.logger.Err(err).Msg("connection error")

		return
	}

	defer cli.Close()

	err = cli.Auth()
	if err != nil {
		s.logger.Err(err).Msg("auth error")

		return
	}

	for _, sess := range s.orderBook.Sessions {
		// cleanup orders
		if atomic.LoadInt64(&sess.ActiveBuyOrderID) > 1 {
			if o := s.storage.GetUserOrder(atomic.LoadInt64(&sess.ActiveBuyOrderID)); o != nil &&
				o.State < dictionary.StateDone {
				err := s.cancelOrder(cli, atomic.LoadInt64(&sess.ActiveBuyOrderID))
				s.logger.Err(err).Int64("oid", atomic.LoadInt64(&sess.ActiveBuyOrderID)).Msg("cancel active buy order")
				atomic.StoreInt64(&sess.PrevBuyOrderID, o.ID)
				atomic.StoreInt64(&sess.ActiveBuyOrderID, 0) // need to create new buy order after restart
			}
		}

		if atomic.LoadInt64(&sess.ActiveSellOrderID) > 1 {
			if o := s.storage.GetUserOrder(atomic.LoadInt64(&sess.ActiveSellOrderID)); o != nil &&
				o.State < dictionary.StateDone {
				err := s.cancelOrder(cli, atomic.LoadInt64(&sess.ActiveSellOrderID))
				s.logger.Err(err).Int64("oid", atomic.LoadInt64(&sess.ActiveSellOrderID)).Msg("cancel active sell order")
				atomic.StoreInt64(&sess.PrevSellOrderID, o.ID)
				atomic.StoreInt64(&sess.ActiveSellOrderID, 1) // need to create new sell order after restart
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

	if atomic.LoadInt64(&sess.PrevBuyOrderID) == 0 {
		total, err = s.getTotalBuyVolume()
		if err != nil {
			s.logger.Err(err).Msg("parse string as float")

			return nil, nil, nil, err
		}

		amount.Quo(total, price)

		return amount, total, price, nil
	}

	prevBuyOrder := s.storage.GetUserOrder(atomic.LoadInt64(&sess.PrevBuyOrderID))

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
	buyOrder := s.storage.GetUserOrder(atomic.LoadInt64(&sess.ActiveBuyOrderID))

	var ok bool

	if atomic.LoadInt64(&sess.PrevSellOrderID) == 0 {
		amount, ok = s.storage.GetTotalBuyVolume(s.pair, sess.ID)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Int64("oid", buyOrder.ID).
				Int64("prev oid", atomic.LoadInt64(&sess.PrevSellOrderID)).
				Msg("parse string as float")

			return nil, nil, nil, dictionary.ErrParseFloat
		}
	} else {
		prevSellOrder := s.storage.GetUserOrder(atomic.LoadInt64(&sess.PrevSellOrderID))
		amount = prevSellOrder.OrderedVolume

		if prevSellOrder.TotalSellVolume.Cmp(dictionary.ZeroBigFloat) != 0 {
			newAmount := amount.Sub(amount, prevSellOrder.TotalSellVolume)
			amount = newAmount
		}
	}

	total.Mul(amount, price)

	return amount, total, price, nil
}
