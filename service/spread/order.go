package spread

import (
	"context"
	"errors"
	"math/big"
	"strconv"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
	storageSpread "github.com/soulgarden/kickex-bot/storage/spread"
)

type Order struct {
	pair      *storage.Pair
	orderBook *storage.Book
	storage   *storage.Storage
	wsSvc     *service.WS
	orderSvc  *service.Order
	sessSvc   *Session
	tgSvc     *Tg

	priceStep          *big.Float
	spreadForStartSell *big.Float

	forceCheckBroker *broker.Broker

	logger *zerolog.Logger
}

func NewOrder(
	cfg *conf.Bot,
	pair *storage.Pair,
	orderBook *storage.Book,
	storage *storage.Storage,
	wsSvc *service.WS,
	orderSvc *service.Order,
	sessSvc *Session,
	tgSvc *Tg,
	priceStep *big.Float,
	forceCheckBroker *broker.Broker,
	logger *zerolog.Logger,
) (*Order, error) {
	spreadForStartSell, ok := big.NewFloat(0).SetString(cfg.Spread.SpreadForStartSell)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", cfg.Spread.SpreadForStartSell).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Order{
		pair:               pair,
		orderBook:          orderBook,
		storage:            storage,
		wsSvc:              wsSvc,
		orderSvc:           orderSvc,
		sessSvc:            sessSvc,
		tgSvc:              tgSvc,
		priceStep:          priceStep,
		spreadForStartSell: spreadForStartSell,
		forceCheckBroker:   forceCheckBroker,
		logger:             logger,
	}, nil
}

func (s *Order) createBuyOrder(sess *storageSpread.Session) error {
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

func (s *Order) createSellOrder(sess *storageSpread.Session) error {
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
	sess.SetActiveSellOrderRequestID(strconv.FormatInt(rid, dictionary.DefaultIntBase))

	return nil
}

func (s *Order) moveBuyOrder(sess *storageSpread.Session) (int64, string, error) {
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

func (s *Order) moveSellOrder(sess *storageSpread.Session, orderID int64) (int64, string, error) {
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

func (s *Order) SetBuyOrderExecutedFlags(sess *storageSpread.Session, order *storage.Order) error {
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
		}

		return err
	}

	sess.SetBuyOrderDoneFlags(order.ID, s.sessSvc.GetSessTotalBoughtVolume(sess))

	s.orderBook.SubProfit(s.sessSvc.GetSessTotalBoughtCost(sess))
	s.forceCheckBroker.Publish(struct{}{})

	s.tgSvc.sendTGAsyncOrderReachedDoneState(sess, order)

	return nil
}

func (s *Order) SetSellOrderExecutedFlags(sess *storageSpread.Session, order *storage.Order) error {
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
				Msg("cancel sell order")

			return err
		}
	}

	sess.SetSellOrderDoneFlags()

	s.orderBook.AddProfit(s.sessSvc.GetSessTotalSoldCost(sess))

	soldVolume := s.sessSvc.GetSessTotalSoldVolume(sess)
	boughtVolume := s.sessSvc.GetSessTotalBoughtVolume(sess)

	s.forceCheckBroker.Publish(struct{}{})

	s.tgSvc.SellOrderReachedDoneState(sess, order, soldVolume, boughtVolume)

	return nil
}

func (s *Order) UpdateOrderStateByID(ctx context.Context, oid int64) error {
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

func (s *Order) calcBuySpread(activeBuyOrderID int64) *big.Float {
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

func (s *Order) calculateBuyOrderVolume(sess *storageSpread.Session) (amount, total, price *big.Float) {
	price = big.NewFloat(0).Add(s.orderBook.GetMaxBidPrice(), s.priceStep)
	total = big.NewFloat(0).Sub(sess.GetBuyTotal(), s.sessSvc.GetSessTotalBoughtCost(sess))
	amount = big.NewFloat(0)

	amount.Quo(total, price)

	return amount, total, price
}

func (s *Order) calculateSellOrderVolume(sess *storageSpread.Session) (amount, total, price *big.Float) {
	price = big.NewFloat(0).Sub(s.orderBook.GetMinAskPrice(), s.priceStep)
	total = big.NewFloat(0)
	amount = big.NewFloat(0)

	amount.Sub(sess.GetSellVolume(), s.sessSvc.GetSessTotalSoldVolume(sess))

	total.Mul(amount, price)

	return amount, total, price
}
