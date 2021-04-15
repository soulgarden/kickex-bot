package subscriber

import (
	"context"
	"encoding/json"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Order struct {
	cfg                 *conf.Bot
	pair                *conf.Pair
	storage             *storage.Storage
	priceStep           *big.Float
	spreadForStartTrade *big.Float
	spreadForStopTrade  *big.Float
	logger              *zerolog.Logger
}

func NewOrder(cfg *conf.Bot, storage *storage.Storage, logger *zerolog.Logger) *Order {
	pair, ok := cfg.Pairs[cfg.Pair]
	if !ok {
		logger.Fatal().Msg("pair is missing in pairs list")
	}

	priceStep, ok := big.NewFloat(0).SetString(pair.PriceStep)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	spreadForStartTrade, ok := big.NewFloat(0).SetString(pair.SpreadForStartTrade)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	spreadForStopTrade, ok := big.NewFloat(0).SetString(pair.SpreadForStopTrade)
	if !ok {
		logger.Fatal().Str("val", pair.PriceStep).Msg("parse string as float error")
	}

	return &Order{
		cfg:                 cfg,
		pair:                cfg.Pairs[cfg.Pair],
		storage:             storage,
		priceStep:           priceStep,
		spreadForStartTrade: spreadForStartTrade,
		spreadForStopTrade:  spreadForStopTrade,
		logger:              logger,
	}
}

func (s *Order) Start(ctx context.Context, eventCh chan int) error {
	orderCtx, cancel := context.WithCancel(ctx)
	cli, err := client.NewWsCli(s.cfg, s.logger)

	if err != nil {
		s.logger.Err(err).Msg("connection error")

		cancel()

		return err
	}

	defer cli.Close()

	go s.orderCreationDecider(orderCtx, eventCh, cli)
	go s.listenNewOrders(orderCtx, eventCh, cli)

	<-ctx.Done()
	cancel()

	if s.storage.Book.ActiveBuyOrderID > 1 {
		err := s.cancelOrder(cli, s.storage.Book.ActiveBuyOrderID)
		s.logger.Err(err).Int64("oid", s.storage.Book.ActiveBuyOrderID).Msg("cancel order")
	}

	if s.storage.Book.ActiveSellOrderID > 1 {
		err := s.cancelOrder(cli, s.storage.Book.ActiveSellOrderID)
		s.logger.Err(err).Int64("oid", s.storage.Book.ActiveSellOrderID).Msg("cancel order")
	}

	return nil
}

func (s *Order) orderCreationDecider(ctx context.Context, e <-chan int, cli *client.Client) {
	for {
		select {
		case <-e:
			if s.storage.Book.CompletedBuyOrders == s.cfg.MaxCompletedOrders &&
				s.storage.Book.CompletedSellOrders == s.cfg.MaxCompletedOrders {
				s.logger.Warn().Msg("max complete orders reached")

				return
			}

			buyOrderDecision := s.storage.Book.Spread.Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.storage.Book.Spread.Cmp(s.spreadForStartTrade) == 1 &&
				s.storage.Book.ActiveSellOrderID == 0 &&
				s.storage.Book.ActiveBuyOrderID == 0

			// need to create buy order
			if buyOrderDecision {
				s.createBuyOrder(cli)
			}

			sellOrderDecision := s.storage.Book.ActiveBuyOrderID > 1 &&
				s.storage.Book.ActiveSellOrderID == 1 &&
				s.calcSellSpread().Cmp(dictionary.ZeroBigFloat) == 1 &&
				s.calcSellSpread().Cmp(s.spreadForStartTrade) == 1

			// need to create sell order
			if sellOrderDecision {
				s.createSellOrder(cli)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Order) listenNewOrders(ctx context.Context, e chan<- int, cli *client.Client) {
	for {
		select {
		case msg := <-cli.ReadCh:
			s.logger.Warn().
				Bytes("payload", msg).
				Msg("Got message")

			er := &response.Error{}

			err := json.Unmarshal(msg, er)
			if err != nil {
				s.logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")
			}

			if er.Error != nil {
				s.logger.Fatal().Bytes("response", msg).Err(err).Msg("received error")
			}

			co := &response.CreatedOrder{}

			err = json.Unmarshal(msg, co)
			if err != nil {
				s.logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")
			}

			go s.manageOrder(ctx, co.OrderID, e)

		case <-ctx.Done():
			return
		}
	}
}

func (s *Order) createBuyOrder(cli *client.Client) {
	atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 1)

	price := big.NewFloat(0).Add(s.storage.Book.GetMaxBidPrice(), s.priceStep)
	total := big.NewFloat(s.pair.TotalBuyAmountInUSDT)
	amount := big.NewFloat(0)

	amount.Quo(total, price)

	s.logger.
		Warn().
		Str("spread", s.storage.Book.Spread.Text('f', s.pair.PricePrecision)).
		Str("price", price.Text('f', s.pair.PricePrecision)).
		Str("amount", amount.Text('f', s.pair.OrderVolumePrecision)).
		Str("total", total.String()).
		Msg("time to place buy order")

	var err error

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

func (s *Order) createSellOrder(cli *client.Client) {
	price := big.NewFloat(0)
	price.Sub(s.storage.Book.GetMinAskPrice(), s.priceStep)

	amount, ok := big.NewFloat(0).SetString(s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID].TotalBuyVolume)
	if !ok {
		s.logger.Fatal().
			Str("val", s.storage.UserOrders[s.storage.Book.ActiveBuyOrderID].TotalBuyVolume).
			Msg("parse total buy volume")
	}

	total := big.NewFloat(0)

	total.Mul(amount, price)

	spread := s.calcSellSpread()

	s.logger.
		Warn().
		Str("price", price.Text('f', s.pair.PricePrecision)).
		Str("amount", amount.Text('f', s.pair.OrderVolumePrecision)).
		Str("total", total.String()).
		Str("spread", spread.String()).
		Msg("time to place sell order")

	var err error

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

func (s *Order) manageOrder(ctx context.Context, orderID int64, e chan<- int) {
	s.logger.Warn().Int64("oid", orderID).Msg("start order manager process")

	defer func() {
		s.logger.Warn().Int64("oid", orderID).Msg("stop order manager process")
	}()

	cli, err := client.NewWsCli(s.cfg, s.logger)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("connection error")
	}

	defer cli.Close()

	timer := time.NewTimer(time.Millisecond * time.Duration(s.cfg.OrderSleepMS))
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
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
						atomic.AddInt64(&s.storage.Book.CompletedBuyOrders, 1)
						atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order
					} else if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
						atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0)
					}
				} else if order.TradeIntent == dictionary.SellBase {
					s.logger.Warn().
						Int("state", order.State).
						Int64("id", orderID).
						Msg("sell order reached final state")

					if order.State == dictionary.StateDone {
						atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0)
						atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 0)
						atomic.AddInt64(&s.storage.Book.CompletedSellOrders, 1)
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

					err := cli.CancelOrder(orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order")
					}

					msg := <-cli.ReadCh

					s.logger.Info().
						Int64("oid", orderID).
						Bytes("payload", msg).
						Msg("cancel buy order response")

					er := &response.Error{}

					err = json.Unmarshal(msg, er)
					if err != nil {
						s.logger.Fatal().Err(err).Msg("unmarshall")
					}

					if er.Error != nil {
						s.logger.Error().Bytes("payload", msg).Msg("can't cancel buy order")

						continue
					}

					atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0)

					return
				}

				// move buy order
				buyOrderPrice, ok := big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Fatal().Str("val", order.LimitPrice).Msg("parse float as string")
				}

				previousPossiblePrice := big.NewFloat(0).Sub(buyOrderPrice, s.priceStep)

				_, prevPriceExists := s.storage.Book.Bids[previousPossiblePrice.Text('f', s.pair.PricePrecision)]
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

					err := cli.CancelOrder(orderID)
					if err != nil {
						s.logger.Fatal().Int64("oid", orderID).Msg("cancel buy order for move")
					}

					msg := <-cli.ReadCh

					s.logger.Warn().
						Int64("oid", orderID).
						Bytes("payload", msg).
						Msg("cancel buy order response")

					er := &response.Error{}

					err = json.Unmarshal(msg, er)
					if err != nil {
						s.logger.Fatal().Err(err).Msg("unmarshall")
					}

					if er.Error != nil {
						s.logger.Error().Bytes("payload", msg).Msg("can't cancel buy order")

						continue
					}

					atomic.StoreInt64(&s.storage.Book.ActiveBuyOrderID, 0)

					e <- 0 // don't wait change order book

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
						continue
					}

					atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order

					return
				}

				// move sell order
				sellOrderPrice, ok := big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Fatal().Str("price", order.LimitPrice).Msg("parse limit price")
				}

				previousPossiblePrice := big.NewFloat(0).Add(sellOrderPrice, s.priceStep)

				_, prevPriceExists := s.storage.Book.Asks[previousPossiblePrice.Text('f', s.pair.PricePrecision)]
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
						continue
					}

					atomic.StoreInt64(&s.storage.Book.ActiveSellOrderID, 1) // need to create new sell order

					e <- 0 // don't wait change order book

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

	msg := <-cli.ReadCh

	s.logger.Info().
		Int64("oid", orderID).
		Bytes("payload", msg).
		Msg("cancel order response")

	er := &response.Error{}

	err = json.Unmarshal(msg, er)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("unmarshall")
	}

	if er.Error != nil {
		s.logger.Error().Bytes("payload", msg).Msg("can't cancel order")

		return err
	}

	return nil
}
