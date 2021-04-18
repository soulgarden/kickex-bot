package subscriber

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Order struct {
	cfg                *conf.Bot
	pair               *conf.Pair
	storage            *storage.Storage
	eventBroker        *Broker
	priceStep          *big.Float
	spreadForStartBuy  *big.Float
	spreadForStartSell *big.Float
	spreadForStopTrade *big.Float
	logger             *zerolog.Logger
}

func NewOrder(cfg *conf.Bot, storage *storage.Storage, eventBroker *Broker, logger *zerolog.Logger) (*Order, error) {
	pair, ok := cfg.Pairs[cfg.Pair]
	if !ok {
		logger.Err(dictionary.ErrInvalidPair).Msg(dictionary.ErrInvalidPair.Error())

		return nil, dictionary.ErrInvalidPair
	}

	priceStep, ok := big.NewFloat(0).SetString(pair.PriceStep)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	spreadForStartTrade, ok := big.NewFloat(0).SetString(pair.SpreadForStartBuy)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	spreadForStartSell, ok := big.NewFloat(0).SetString(pair.SpreadForStartSell)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	spreadForStopTrade, ok := big.NewFloat(0).SetString(pair.SpreadForStopTrade)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	return &Order{
		cfg:                cfg,
		pair:               cfg.Pairs[cfg.Pair],
		storage:            storage,
		eventBroker:        eventBroker,
		priceStep:          priceStep,
		spreadForStartBuy:  spreadForStartTrade,
		spreadForStartSell: spreadForStartSell,
		spreadForStopTrade: spreadForStopTrade,
		logger:             logger,
	}, nil
}

func (s *Order) Start(ctx context.Context, interrupt chan os.Signal, st *storage.Storage) error {
	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)

	if err != nil {
		s.logger.Err(err).Msg("connection error")

		interrupt <- syscall.SIGSTOP

		return err
	}

	defer cli.Close()

	go s.orderCreationDecider(ctx, cli)

	go func() {
		if err := s.listenNewOrders(ctx, interrupt, cli, st); err != nil {
			interrupt <- syscall.SIGSTOP
		}
	}()

	<-ctx.Done()

	// cleanup orders
	if s.storage.Book.ActiveBuyOrderID > 1 {
		if o, ok := s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID]; ok && o.State < dictionary.StateDone {
			err := s.cancelOrder(cli, s.storage.Book.ActiveBuyOrderID)
			s.logger.Err(err).Int64("oid", s.storage.Book.ActiveBuyOrderID).Msg("try to cancel active buy order")
		}
	}

	if s.storage.Book.ActiveSellOrderID > 1 {
		if o, ok := s.storage.UserOrders[s.storage.Book.ActiveSellOrderID]; ok && o.State < dictionary.StateDone {
			err := s.cancelOrder(cli, s.storage.Book.ActiveSellOrderID)
			s.logger.Err(err).Int64("oid", s.storage.Book.ActiveSellOrderID).Msg("try to cancel active sell order")
		}
	}

	return nil
}

func (s *Order) orderCreationDecider(ctx context.Context, cli *client.Client) {
	e := s.eventBroker.Subscribe()
	defer s.eventBroker.Unsubscribe(e)

	for {
		select {
		case <-e:
			if s.storage.Book.CompletedBuyOrders == s.cfg.MaxCompletedOrders &&
				s.storage.Book.CompletedSellOrders == s.cfg.MaxCompletedOrders {
				s.logger.Warn().Msg("max complete orders reached")

				return
			}

			buyOrderDecision := s.storage.Book.Spread.Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.storage.Book.Spread.Cmp(s.spreadForStartBuy) == 1 &&
				s.storage.Book.ActiveSellOrderID == 0 &&
				s.storage.Book.ActiveBuyOrderID == 0

			// need to create buy order
			if buyOrderDecision {
				s.createBuyOrder(cli, s.storage.Book.PrevBuyOrderID)
			}

			sellOrderDecision := s.storage.Book.ActiveBuyOrderID > 1 &&
				s.storage.Book.ActiveSellOrderID == 1 &&
				s.calcSellSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.calcSellSpread().Cmp(s.spreadForStartSell) == 1

			// need to create sell order
			if sellOrderDecision {
				s.createSellOrder(cli, s.storage.Book.PrevSellOrderID)
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
	st *storage.Storage,
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

			skip, attempts, err = s.checkListenOrderErrors(st, msg, attempts)
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

			go s.manageOrder(ctx, interrupt, co.OrderID)

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Order) checkListenOrderErrors(st *storage.Storage, msg []byte, attempts int) (bool, int, error) {
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
		if er.Error.Reason == response.AmountTooSmall && (s.storage.Book.PrevBuyOrderID != 0 || s.storage.Book.PrevSellOrderID != 0) {
			if id == st.Book.ActiveBuyOrderID {
				s.logger.Warn().
					Int64("prev oid", s.storage.Book.PrevBuyOrderID).
					Int64("oid", st.Book.ActiveBuyOrderID).
					Msg("consider prev buy order as executed, allow to place sell order")

				s.setBuyOrderExecutedFlags(s.storage.Book.PrevBuyOrderID)
			} else if id == st.Book.ActiveSellOrderID {
				s.logger.Warn().
					Int64("prev oid", s.storage.Book.PrevSellOrderID).
					Int64("oid", st.Book.ActiveSellOrderID).
					Msg("consider prev sell order as executed, allow to place buy order")

				s.setSellOrderExecutedFlags()
			}

			return true, attempts, nil
		}

		if er.Error.Code == response.InsufficientFundsCode {
			if id == st.Book.ActiveSellOrderID && attempts < 10 {
				atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order

				return true, attempts, nil
			} else if id == st.Book.ActiveBuyOrderID && attempts < 10 {
				atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0) // need to create new buy order

				return true, attempts, nil
			}
		}

		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return false, attempts, nil
	}

	return false, 0, nil
}

func (s *Order) createBuyOrder(cli *client.Client, prevOrderId int64) {
	atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 1)

	price := big.NewFloat(0).Add(s.storage.Book.GetMaxBidPrice(), s.priceStep)
	total := big.NewFloat(0)
	amount := big.NewFloat(0)

	if prevOrderId == 0 {
		total = total.SetFloat64(s.pair.TotalBuyAmountInUSDT)
		amount.Quo(total, price)
	} else {
		prevBuyOrder := s.storage.UserOrders[prevOrderId]
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
		Int64("prev order id", prevOrderId).
		Str("spread", s.storage.Book.Spread.Text('f', s.pair.PricePrecision)).
		Str("price", price.Text('f', s.pair.PricePrecision)).
		Str("amount", amount.Text('f', s.pair.OrderVolumePrecision)).
		Str("total", total.String()).
		Msg("time to place buy order")

	var err error

	// todo subscribe to balance updates for ensure that assets is enough
	extID, err := cli.CreateOrder(
		s.cfg.Pair,
		amount.Text('f', s.pair.OrderVolumePrecision),
		price.Text('f', s.pair.PricePrecision),
		dictionary.BuyBase,
	)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("create buy order")
	}

	atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, extID)
}

func (s *Order) createSellOrder(cli *client.Client, prevOrderId int64) {
	price := big.NewFloat(0)
	price.Sub(s.storage.Book.GetMinAskPrice(), s.priceStep)

	buyOrder := s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID]

	var ok bool

	amount := big.NewFloat(0)

	if prevOrderId == 0 {
		amount, ok = amount.SetString(buyOrder.TotalBuyVolume)
		if !ok {
			s.logger.Fatal().
				Str("val", buyOrder.TotalBuyVolume).
				Msg("parse string as float")
		}
	} else {
		prevSellOrder := s.storage.UserOrders[prevOrderId]
		amount, ok = amount.SetString(prevSellOrder.OrderedVolume)
		if !ok {
			s.logger.Fatal().
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

	spread := s.calcSellSpread()

	s.logger.
		Warn().
		Int64("prev order id", prevOrderId).
		Str("price", price.Text('f', s.pair.PricePrecision)).
		Str("amount", amount.Text('f', s.pair.OrderVolumePrecision)).
		Str("total", total.String()).
		Str("spread", spread.String()).
		Msg("time to place sell order")

	var err error

	// todo subscribe to balance updates for ensure that assets is enough
	extID, err := cli.CreateOrder(
		s.cfg.Pair,
		amount.Text('f', s.pair.OrderVolumePrecision),
		price.Text('f', s.pair.PricePrecision),
		dictionary.SellBase,
	)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("create sell order")
	}

	atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, extID)
}

func (s *Order) manageOrder(ctx context.Context, interrupt chan os.Signal, orderID int64) {
	s.logger.Warn().Int64("oid", orderID).Msg("start order manager process")

	e := s.eventBroker.Subscribe()

	defer func() {
		s.logger.Warn().Int64("oid", orderID).Msg("stop order manager process")
		s.eventBroker.Unsubscribe(e)
	}()

	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("connection error")
	}

	defer cli.Close()

	for {
		select {
		case <-e:
			// order sent, wait creation
			order, ok := s.storage.UserOrders[orderID]
			if !ok {
				continue
			}

			if order.TradeIntent == dictionary.BuyBase {
				atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, orderID)
			} else if order.TradeIntent == dictionary.SellBase {
				atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, orderID)
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
						s.setBuyOrderExecutedFlags(s.storage.Book.ActiveBuyOrderID)
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0) // need to create new buy order
						atomic.StoreInt64(&s.storage.Book.PrevBuyOrderID, orderID)
					}
				} else if order.TradeIntent == dictionary.SellBase {
					s.logger.Warn().
						Int("state", order.State).
						Int64("id", orderID).
						Msg("sell order reached final state")

					if order.State == dictionary.StateDone {
						s.setSellOrderExecutedFlags()
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order
					}
				}

				return
			}

			if order.TradeIntent == dictionary.BuyBase {
				spread := s.calcBuySpread()
				// cancel buy order
				if spread.Cmp(s.spreadForStopTrade) == -1 && s.storage.Book.Spread.Cmp(dictionary.ZeroBigFloat) == 1 {
					s.logger.Warn().
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel buy order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order")
					}

					atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0) // need to create new buy order
					atomic.StoreInt64(&s.storage.Book.PrevBuyOrderID, orderID)

					return
				}

				// move buy order
				buyOrderPrice, ok := big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Fatal().Str("val", order.LimitPrice).Msg("parse float as string")
				}

				previousPossiblePrice := big.NewFloat(0).Sub(buyOrderPrice, s.priceStep)

				prevPriceExists := s.storage.Book.GetBid(previousPossiblePrice.Text('f', s.pair.PricePrecision)) != nil
				nextPriceExists := s.storage.Book.GetMaxBidPrice().Cmp(buyOrderPrice) == 1

				if nextPriceExists || !prevPriceExists {
					s.logger.Warn().
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Str("order price", buyOrderPrice.Text('f', s.pair.PricePrecision)).
						Str("max bid price", s.storage.Book.GetMaxBidPrice().Text('f', s.pair.PricePrecision)).
						Bool("larger bid price exists", nextPriceExists).
						Str("prev possible bid price", previousPossiblePrice.Text('f', -1)).
						Bool("prev possible bid price exists", prevPriceExists).
						Msg("time to move buy order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order for move")
					}

					atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0) // need to create new buy order
					atomic.StoreInt64(&s.storage.Book.PrevBuyOrderID, orderID)

					s.eventBroker.Publish(0) // don't wait change order book

					return
				}
			}

			if order.TradeIntent == dictionary.SellBase {
				spread := s.calcSellSpread()

				// cancel sell order
				if spread.Cmp(s.spreadForStopTrade) == -1 {
					s.logger.Warn().
						Int64("oid", orderID).
						Str("spread", spread.String()).
						Msg("time to cancel sell order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
					}

					atomic.StoreInt64(&s.storage.Book.PrevSellOrderID, orderID)
					atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order

					return
				}

				// move sell order
				sellOrderPrice, ok := big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Fatal().Str("price", order.LimitPrice).Msg("parse limit price")
				}

				previousPossiblePrice := big.NewFloat(0).Add(sellOrderPrice, s.priceStep)

				prevPriceExists := s.storage.Book.GetAsk(previousPossiblePrice.Text('f', s.pair.PricePrecision)) != nil
				nextPriceExists := s.storage.Book.GetMinAskPrice().Cmp(sellOrderPrice) == -1

				if nextPriceExists || !prevPriceExists {
					s.logger.Warn().
						Int64("oid", orderID).
						Str("order price", sellOrderPrice.Text('f', s.pair.PricePrecision)).
						Str("min ask price", s.storage.Book.GetMinAskPrice().Text('f', s.pair.PricePrecision)).
						Str("spread", spread.String()).
						Bool("larger ask price exists", nextPriceExists).
						Str("prev possible ask price", previousPossiblePrice.Text('f', -1)).
						Bool("prev possible ask price exists", prevPriceExists).
						Msg("time to move sell order")

					err := s.cancelOrder(cli, orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("can't cancel order")
					}

					atomic.StoreInt64(&s.storage.Book.PrevSellOrderID, orderID)
					atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order

					s.eventBroker.Publish(0) // don't wait change order book

					return
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

func (s *Order) calcSellSpread() *big.Float {
	buyBidPrice, ok := big.NewFloat(0).SetString(s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID].LimitPrice)
	if !ok {
		s.logger.Fatal().
			Str("val", s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID].LimitPrice).
			Msg("parse string as float")
	}

	// 100 - (x * 100 / y)
	return big.NewFloat(0).Sub(
		dictionary.MaxPercentFloat,
		big.NewFloat(0).Quo(
			big.NewFloat(0).Mul(buyBidPrice, dictionary.MaxPercentFloat),
			s.storage.Book.GetMinAskPrice()),
	)
}

func (s *Order) calcBuySpread() *big.Float {
	if _, ok := s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID]; !ok {
		return dictionary.ZeroBigFloat
	}

	buyBidPrice, ok := big.NewFloat(0).SetString(s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID].LimitPrice)
	if !ok {
		s.logger.Fatal().
			Str("val", s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID].LimitPrice).
			Msg("parse string as float")
	}

	// 100 - (x * 100 / y)
	return big.NewFloat(0).Sub(
		dictionary.MaxPercentFloat,
		big.NewFloat(0).Quo(
			big.NewFloat(0).Mul(buyBidPrice, dictionary.MaxPercentFloat),
			s.storage.Book.GetMinAskPrice()),
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

func (s *Order) setBuyOrderExecutedFlags(activeBuyOrderID int64) {
	atomic.AddInt64(&s.storage.Book.CompletedBuyOrders, 1)
	atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, activeBuyOrderID)
	atomic.StoreInt64(&s.storage.Book.PrevBuyOrderID, 0)
	atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order
}

func (s *Order) setSellOrderExecutedFlags() {
	atomic.AddInt64(&s.storage.Book.CompletedSellOrders, 1)
	atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0) // need to create new buy order
	atomic.StoreInt64(&s.storage.Book.PrevSellOrderID, 0)
	atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 0)
}
