package strategy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/soulgarden/kickex-bot/service/arbitrage"

	"golang.org/x/sync/errgroup"

	"github.com/mailru/easyjson"

	spreadSvc "github.com/soulgarden/kickex-bot/service/spread"

	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

const firstStepOrderExecutionDuration = time.Second * 5
const orderExecutionDuration = time.Minute * 5
const lastStepOrderExecutionDuration = time.Minute * 60
const chSize = 1024

type Arbitrage struct {
	cfg            *conf.Bot
	storage        *storage.Storage
	conversion     *service.Conversion
	arbTSvc        *arbitrage.Tg
	wsSvc          *service.WS
	wsEventBroker  *broker.Broker
	accEventBroker *broker.Broker
	orderSvc       *service.Order
	sessSvc        *spreadSvc.Session
	balanceSvc     *service.Balance
	totalBuyInUSDT *big.Float

	logger *zerolog.Logger
}

func NewArbitrage(
	cfg *conf.Bot,
	storage *storage.Storage,
	conversion *service.Conversion,
	arbTSvc *arbitrage.Tg,
	wsSvc *service.WS,
	wsEventBroker *broker.Broker,
	accEventBroker *broker.Broker,
	orderSvc *service.Order,
	sessSvc *spreadSvc.Session,
	balanceSvc *service.Balance,
	logger *zerolog.Logger,
) (*Arbitrage, error) {
	totalBuyInUSDT, ok := big.NewFloat(0).SetString(cfg.Arbitrage.TotalBuyInUSDT)
	if !ok {
		logger.
			Err(dictionary.ErrParseFloat).
			Str("val", cfg.Arbitrage.TotalBuyInUSDT).
			Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Arbitrage{
		cfg:            cfg,
		storage:        storage,
		conversion:     conversion,
		arbTSvc:        arbTSvc,
		wsSvc:          wsSvc,
		wsEventBroker:  wsEventBroker,
		accEventBroker: accEventBroker,
		orderSvc:       orderSvc,
		sessSvc:        sessSvc,
		balanceSvc:     balanceSvc,
		totalBuyInUSDT: totalBuyInUSDT,
		logger:         logger,
	}, nil
}

func (s *Arbitrage) Start(ctx context.Context, g *errgroup.Group) error {
	s.logger.Warn().Msg("arbitrage process started")
	defer s.logger.Warn().Msg("stop arbitrage process")

	ch := make(chan bool, chSize)

	g.Go(func() error { return s.collectEvents(ctx, g, ch) })

	pair := s.storage.GetPair(s.cfg.Arbitrage.Pair)

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return dictionary.ErrEventChannelClosed
			}

			if err := s.check(ctx, pair); err != nil {
				return err
			}

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Arbitrage) collectEvents(ctx context.Context, g *errgroup.Group, ch chan<- bool) error {
	pairs := s.GetPairsList()

	for pairName := range pairs {
		pair := s.storage.GetPair(pairName)

		g.Go(func() error {
			s.logger.Warn().Str("pair", pair.GetPairName()).Msg("start collect events")
			defer s.logger.Warn().Str("pair", pair.GetPairName()).Msg("finish collect events")

			return s.listenPairEvents(ctx, pair, ch)
		})
	}

	return nil
}

func (s *Arbitrage) listenPairEvents(
	ctx context.Context,
	pair *storage.Pair,
	ch chan<- bool,
) error {
	s.logger.Warn().Str("pair", pair.GetPairName()).Msg("start listen pair events")
	defer s.logger.Warn().Str("pair", pair.GetPairName()).Msg("finish listen pair events")

	e := s.storage.GetOrderBook(pair.BaseCurrency, pair.QuoteCurrency).OrderBookEventBroker.Subscribe("listen events")
	defer s.storage.GetOrderBook(pair.BaseCurrency, pair.QuoteCurrency).OrderBookEventBroker.Unsubscribe(e)

	for {
		select {
		case _, ok := <-e:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return dictionary.ErrEventChannelClosed
			}

			ch <- true
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Arbitrage) check(ctx context.Context, pair *storage.Pair) error {
	baseUSDTPair := s.storage.GetPair(pair.BaseCurrency + "/" + dictionary.USDT)

	baseBuyOrder := s.storage.GetOrderBook(baseUSDTPair.BaseCurrency, baseUSDTPair.QuoteCurrency).GetMinAsk()
	baseSellOrder := s.storage.GetOrderBook(baseUSDTPair.BaseCurrency, baseUSDTPair.QuoteCurrency).GetMaxBid()

	quotedUSDTPair := s.storage.GetPair(pair.QuoteCurrency + "/" + dictionary.USDT)
	quotedBuyOrder := s.storage.GetOrderBook(quotedUSDTPair.BaseCurrency, quotedUSDTPair.QuoteCurrency).GetMinAsk()
	quotedSellOrder := s.storage.GetOrderBook(quotedUSDTPair.BaseCurrency, quotedUSDTPair.QuoteCurrency).GetMaxBid()

	book := s.storage.GetOrderBook(pair.BaseCurrency, pair.QuoteCurrency)
	baseQuotedPair := s.storage.GetPair(pair.BaseCurrency + "/" + pair.QuoteCurrency)
	baseQuotedBuyOrder := book.GetMinAsk()
	baseQuotedSellOrder := book.GetMaxBid()

	if baseBuyOrder == nil || baseSellOrder == nil || quotedBuyOrder == nil || quotedSellOrder == nil ||
		baseQuotedBuyOrder == nil || baseQuotedSellOrder == nil {
		return nil
	}

	err := s.checkBuyBaseOption(
		ctx,
		baseUSDTPair,
		baseBuyOrder,
		quotedUSDTPair,
		quotedSellOrder,
		baseQuotedPair,
		baseQuotedSellOrder,
	)

	if err != nil {
		s.arbTSvc.SendTGCheckBuyBaseFinishedWithError(baseQuotedPair, err)

		s.logger.Err(err).Msg("check buy base option finished with error")

		return err
	}

	s.checkBuyQuotedOptions(
		baseUSDTPair,
		quotedBuyOrder,
		quotedUSDTPair,
		baseSellOrder,
		baseQuotedPair,
		baseQuotedBuyOrder,
	)

	return nil
}

func (s *Arbitrage) GetPairsList() map[string]bool {
	pairs := make(map[string]bool)

	pair := strings.Split(s.cfg.Arbitrage.Pair, "/")

	pairs[pair[0]+"/"+pair[1]] = true

	pairs[pair[0]+"/"+dictionary.USDT] = true
	pairs[pair[1]+"/"+dictionary.USDT] = true

	return pairs
}

func (s *Arbitrage) checkBuyBaseOption(
	ctx context.Context,
	baseUSDTPair *storage.Pair,
	baseBuyOrder *storage.BookOrder,
	quotedUSDTPair *storage.Pair,
	quotedSellOrder *storage.BookOrder,
	baseQuotedPair *storage.Pair,
	baseQuotedSellOrder *storage.BookOrder,
) error {
	s.logger.Debug().
		Str("pair", baseQuotedPair.GetPairName()).
		Str("base buy order total", baseBuyOrder.Total.String()).
		Str("base quoted sell order total", baseQuotedSellOrder.Total.String()).
		Str("quoted sell order total", quotedSellOrder.Total.String()).
		Msg("option 1 info")

	startBuyVolume := s.totalBuyInUSDT

	// option 1 // buy base for USDT / sell base for quoted / sell quoted for USDT
	if baseBuyOrder.Total.Cmp(startBuyVolume) == 1 &&
		baseQuotedSellOrder.Total.Cmp(baseQuotedPair.MinVolume) == 1 &&
		quotedSellOrder.Total.Cmp(quotedUSDTPair.MinVolume) == 1 {
		baseBuyOrderAmount := big.NewFloat(0).Quo(startBuyVolume, baseBuyOrder.Price)

		baseQuotedSellOrderTotal := big.NewFloat(0).Mul(baseBuyOrderAmount, baseQuotedSellOrder.Price)

		quotedSellOrderUSDTAmount := big.NewFloat(0).Mul(baseQuotedSellOrderTotal, quotedSellOrder.Price)

		// (x * 100 / y) - 100
		spread := big.NewFloat(0).Sub(
			big.NewFloat(0).
				Quo(big.NewFloat(0).Mul(quotedSellOrderUSDTAmount, dictionary.MaxPercentFloat), startBuyVolume),
			dictionary.MaxPercentFloat,
		)

		s.logger.Info().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("before USDT amount", startBuyVolume.Text('f', dictionary.ExtendedPrecision)).
			Str("after USDT amount", quotedSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision)).
			Str("spread", spread.Text('f', dictionary.DefaultPrecision)).
			Msg("pair spread")

		if spread.Cmp(big.NewFloat(s.cfg.Arbitrage.PercentForStart)) == 1 {
			s.arbTSvc.SendTGBuyBaseArbitrageAvailable(baseQuotedPair, startBuyVolume, quotedSellOrderUSDTAmount, spread)

			// 1. buy base for USDT
			buyBaseOrder, err := s.buyBaseForUSDT(
				ctx,
				baseUSDTPair,
				baseBuyOrder,
				baseBuyOrderAmount,
				startBuyVolume,
			)
			if err != nil || buyBaseOrder == nil {
				return err
			}

			// 2. sell base for quoted
			sellBaseOrder, err := s.sellBaseForQuoted(
				ctx,
				baseQuotedPair,
				buyBaseOrder,
				baseQuotedSellOrder,
				baseQuotedSellOrderTotal,
			)
			if err != nil || sellBaseOrder == nil {
				return err
			}

			// 3. sell quoted for USDT
			go func() {
				sellQuotedOrder, err := s.sellQuotedForUSDT(
					ctx,
					sellBaseOrder,
					quotedUSDTPair,
					quotedSellOrder,
					quotedSellOrderUSDTAmount,
				)
				if err != nil || sellQuotedOrder == nil {
					return
				}

				s.arbTSvc.SendTGSuccess(
					s.cfg.Env,
					baseQuotedPair.GetPairName(),
					big.NewFloat(0).
						Add(buyBaseOrder.TotalSellVolume, buyBaseOrder.TotalFeeQuoted).
						Text('f', baseUSDTPair.VolumeScale),
					sellQuotedOrder.TotalBuyVolume.Text('f', baseUSDTPair.VolumeScale),
				)
			}()
		}
	}

	return nil
}

func (s *Arbitrage) buyBaseForUSDT(
	ctx context.Context,
	baseUSDTPair *storage.Pair,
	baseBuyOrder *storage.BookOrder,
	baseBuyOrderAmount, startBuyVolume *big.Float,
) (*storage.Order, error) {
	s.logger.Warn().
		Str("pair", baseUSDTPair.GetPairName()).
		Str("amount", s.prepareAmount(baseBuyOrderAmount, baseUSDTPair)).
		Str("price", baseBuyOrder.Price.Text('f', baseUSDTPair.PriceScale)).
		Str("total", startBuyVolume.Text('f', baseUSDTPair.QuantityScale)).
		Msg("buy base")

	oid, err := s.createAndWaitExecOrder(
		ctx,
		baseUSDTPair,
		baseBuyOrderAmount,
		baseBuyOrder.Price,
		dictionary.BuyBase,
		firstStepOrderExecutionDuration,
	)
	s.logger.Err(err).Str("pair", baseUSDTPair.GetPairName()).Int64("oid", oid).Msg("create order")

	if err != nil {
		if errors.Is(err, dictionary.ErrOrderNotCompleted) {
			return nil, s.cancelOrder(oid, baseUSDTPair)
		}

		return nil, err
	}

	buyBaseOrder := s.storage.GetUserOrder(oid)

	if buyBaseOrder == nil {
		s.logger.
			Err(dictionary.ErrOrderNotFoundOrOutdated).
			Int64("oid", oid).
			Msg("order not found in order book")

		return nil, dictionary.ErrOrderNotFoundOrOutdated
	}

	return buyBaseOrder, nil
}

func (s *Arbitrage) sellBaseForQuoted(
	ctx context.Context,
	baseQuotedPair *storage.Pair,
	buyBaseOrder *storage.Order,
	baseQuotedSellOrder *storage.BookOrder,
	baseQuotedSellOrderTotal *big.Float,
) (*storage.Order, error) {
	s.logger.Warn().
		Str("pair", baseQuotedPair.GetPairName()).
		Str("amount", s.prepareAmount(buyBaseOrder.TotalBuyVolume, baseQuotedPair)).
		Str("price", baseQuotedSellOrder.Price.Text('f', baseQuotedPair.PriceScale)).
		Str("total", baseQuotedSellOrderTotal.Text('f', baseQuotedPair.VolumeScale)).
		Msg("sell base for quoted")

	oid, err := s.createAndWaitExecOrder(
		ctx,
		baseQuotedPair,
		buyBaseOrder.TotalBuyVolume,
		baseQuotedSellOrder.Price,
		dictionary.SellBase,
		orderExecutionDuration,
	)
	s.logger.Err(err).Str("pair", baseQuotedPair.GetPairName()).Int64("oid", oid).Msg("create order")

	if err != nil {
		if errors.Is(err, dictionary.ErrOrderNotCompleted) {
			s.arbTSvc.SendTGFailed(s.cfg.Env, dictionary.SecondStep, oid, baseQuotedPair.GetPairName())

			return nil, s.cancelOrder(oid, baseQuotedPair) // create order selling what was bought on the first step
		}

		return nil, err
	}

	sellBaseOrder := s.storage.GetUserOrder(oid)
	if sellBaseOrder == nil {
		s.logger.
			Err(dictionary.ErrOrderNotFoundOrOutdated).
			Int64("oid", oid).
			Msg("order not found in order book")

		return nil, dictionary.ErrOrderNotFoundOrOutdated
	}

	return sellBaseOrder, nil
}

func (s *Arbitrage) sellQuotedForUSDT(
	ctx context.Context,
	sellBaseOrder *storage.Order,
	quotedUSDTPair *storage.Pair,
	quotedSellOrder *storage.BookOrder,
	quotedSellOrderUSDTAmount *big.Float,
) (*storage.Order, error) {
	sellVolume := big.NewFloat(0).Sub(sellBaseOrder.TotalBuyVolume, sellBaseOrder.TotalFeeQuoted)

	s.logger.Warn().
		Str("pair", quotedUSDTPair.GetPairName()).
		Str("amount", s.prepareAmount(sellVolume, quotedUSDTPair)).
		Str("price", quotedSellOrder.Price.Text('f', quotedUSDTPair.PriceScale)).
		Str("total", quotedSellOrderUSDTAmount.Text('f', quotedUSDTPair.VolumeScale)).
		Msg("sell quoted")

	oid, err := s.createAndWaitExecOrder(
		ctx,
		quotedUSDTPair,
		sellVolume,
		quotedSellOrder.Price,
		dictionary.SellBase,
		lastStepOrderExecutionDuration,
	)
	s.logger.Err(err).Str("pair", quotedUSDTPair.GetPairName()).Int64("oid", oid).Msg("create order")

	if err != nil {
		if errors.Is(err, dictionary.ErrOrderNotCompleted) {
			s.arbTSvc.SendTGFailed(s.cfg.Env, dictionary.ThirdStepStep, oid, quotedUSDTPair.GetPairName())

			return nil, s.cancelOrder(oid, quotedUSDTPair)
		}

		return nil, err
	}

	sellQuotedOrder := s.storage.GetUserOrder(oid)
	if sellQuotedOrder == nil {
		s.logger.
			Err(dictionary.ErrOrderNotFoundOrOutdated).
			Int64("oid", oid).
			Msg("order not found in order book")

		return nil, dictionary.ErrOrderNotFoundOrOutdated
	}

	return sellQuotedOrder, nil
}

func (s *Arbitrage) checkBuyQuotedOptions(
	baseUSDTPair *storage.Pair,
	quotedBuyOrder *storage.BookOrder,
	quotedUSDTPair *storage.Pair,
	baseSellOrder *storage.BookOrder,
	baseQuotedPair *storage.Pair,
	baseQuotedBuyOrder *storage.BookOrder,
) {
	s.logger.Debug().
		Str("pair", baseQuotedPair.GetPairName()).
		Str("quoted buy order total", quotedBuyOrder.Total.String()).
		Str("base quoted buy order total", baseQuotedBuyOrder.Total.String()).
		Str("base sell order total", baseSellOrder.Total.String()).
		Msg("option 2 info")

	// option 2 // buy quoted for USDT / buy base for quoted / sell base for USDT
	if quotedBuyOrder.Total.Cmp(quotedUSDTPair.MinVolume) == 1 &&
		baseQuotedBuyOrder.Total.Cmp(baseQuotedPair.MinVolume) == 0 &&
		baseSellOrder.Total.Cmp(baseUSDTPair.MinVolume) == 0 {
		quotedBuyOrderAmount := big.NewFloat(0).Quo(quotedUSDTPair.MinVolume, quotedBuyOrder.Price)

		baseQuotedBuyOrderTotal := big.NewFloat(0).Mul(quotedBuyOrderAmount, baseQuotedBuyOrder.Price)

		baseSellOrderUSDTAmount := big.NewFloat(0).Mul(baseQuotedBuyOrderTotal, baseSellOrder.Price)

		// (x * 100 / y) - 100
		spread := big.NewFloat(0).Sub(
			big.NewFloat(0).
				Quo(big.NewFloat(0).Mul(baseSellOrderUSDTAmount, dictionary.MaxPercentFloat), quotedUSDTPair.MinVolume),
			dictionary.MaxPercentFloat,
		)

		s.logger.Info().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("before USDT amount", quotedUSDTPair.MinVolume.Text('f', dictionary.ExtendedPrecision)).
			Str("after USDT amount", baseSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision)).
			Str("spread", spread.Text('f', dictionary.DefaultPrecision)).
			Msg("pair spread")

		if spread.Cmp(big.NewFloat(s.cfg.Arbitrage.PercentForStart)) == 1 {
			s.arbTSvc.SendTGBuyQuotedArbitrageAvailable(baseQuotedPair, quotedUSDTPair, baseSellOrderUSDTAmount, spread)
		}
	}
}

func (s *Arbitrage) createAndWaitExecOrder(
	ctx context.Context,
	pair *storage.Pair,
	amount, price *big.Float,
	intent int,
	waitExecDuration time.Duration,
) (int64, error) {
	oid, err := s.sendCreateOrderRequest(ctx, pair, amount, price, intent)
	s.logger.Err(err).Msg("send create order request")

	if err != nil {
		return 0, err
	}

	isCompleted, err := s.watchOrder(ctx, oid, pair, waitExecDuration)
	s.logger.Err(err).Int64("oid", oid).Bool("is completed", isCompleted).Msg("watch order")

	if err != nil {
		return oid, err
	}

	if !isCompleted {
		return oid, dictionary.ErrOrderNotCompleted
	}

	return oid, nil
}

func (s *Arbitrage) checkCreateOrderError(msg []byte) error {
	er := &response.Error{}

	err := easyjson.Unmarshal(msg, er)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return err
	}

	if er.Error != nil {
		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)

		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return err
	}

	return nil
}

func (s *Arbitrage) sendCreateOrderRequest(
	ctx context.Context,
	pair *storage.Pair,
	amount *big.Float,
	price *big.Float,
	intent int,
) (oid int64, err error) {
	eventsCh := s.wsEventBroker.Subscribe("send create order request")
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	//todo: move to order service
	reqID, _, err := s.wsSvc.CreateOrder(
		pair.GetPairName(),
		s.prepareAmount(amount, pair),
		price.Text('f', pair.PriceScale),
		intent,
	)

	if err != nil {
		s.logger.Err(err).Msg("create order")

		return 0, err
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				return 0, dictionary.ErrEventChannelClosed
			}

			msg, ok := e.([]byte)
			if !ok {
				return 0, dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}
			err := easyjson.Unmarshal(msg, rid)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return 0, err
			}

			if strconv.FormatInt(reqID, dictionary.DefaultIntBase) != rid.ID {
				continue
			}

			s.logger.Warn().
				Bytes("payload", msg).
				Msg("got message")

			err = s.checkCreateOrderError(msg)
			if err != nil {
				return 0, err
			}

			co := &response.CreatedOrder{}

			err = easyjson.Unmarshal(msg, co)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return 0, err
			}

			return co.OrderID, nil

		case <-ctx.Done():
			return 0, nil
		}
	}
}

func (s *Arbitrage) watchOrder(
	ctx context.Context,
	orderID int64,
	pair *storage.Pair,
	waitExecDuration time.Duration,
) (isCompleted bool, err error) {
	accEventCh := s.accEventBroker.Subscribe("watch order")

	defer s.accEventBroker.Unsubscribe(accEventCh)

	s.logger.Warn().
		Int64("oid", orderID).
		Str("pair", pair.GetPairName()).
		Msg("start watch order process")

	defer s.logger.Warn().
		Int64("oid", orderID).
		Str("pair", pair.GetPairName()).
		Msg("stop watch order process")

	startedTime := time.Now()

	for {
		select {
		case _, ok := <-accEventCh:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return false, dictionary.ErrEventChannelClosed
			}

			//todo: check partial completion
			// order sent, waiting for creation
			order := s.storage.GetUserOrder(orderID)
			if order == nil {
				s.logger.Warn().Int64("oid", orderID).Msg("order not found")

				if startedTime.Add(orderCreationDuration).Before(time.Now()) {
					s.logger.Err(dictionary.ErrOrderCreationEventNotReceived).Msg("order creation event not received")

					s.arbTSvc.SendTGOrderCreationEventNotReceived(orderID)

					return false, dictionary.ErrOrderCreationEventNotReceived
				}

				continue
			}

			if order.State < dictionary.StateActive {
				s.logger.Warn().
					Int64("oid", orderID).
					Int("state", order.State).
					Msg("order state is below active")

				continue
			}

			if order.State == dictionary.StateActive {
				s.logger.Warn().
					Int64("oid", orderID).
					Int("state", order.State).
					Msg("order state is still active")

				continue
			}

			if order.State == dictionary.StateDone {
				s.logger.Warn().Int64("oid", orderID).Msg("order state is done")

				return true, nil
			}

			if order.State == dictionary.StateCancelled || order.State == dictionary.StateRejected {
				s.logger.Warn().Int64("oid", orderID).Msg("order state is cancelled/rejected")

				return false, nil
			}
		case <-time.After(waitExecDuration):
			s.logger.Warn().Int64("oid", orderID).Msg("order state is not executed")

			return false, dictionary.ErrOrderNotCompleted
		case <-ctx.Done():
			return false, nil
		}
	}
}

func (s *Arbitrage) cancelOrder(orderID int64, pair *storage.Pair) error {
	s.logger.Warn().Int64("oid", orderID).Msg("order state is not executed, cancel")

	err := s.orderSvc.CancelOrder(orderID)
	if err != nil {
		if errors.Is(err, dictionary.ErrCantCancelDoneOrder) {
			s.logger.Warn().
				Str("pair", pair.GetPairName()).
				Int64("oid", orderID).
				Msg("expected cancelled state, but got done")

			return nil
		}

		s.logger.Err(err).
			Str("pair", pair.GetPairName()).
			Int64("oid", orderID).
			Msg("cancel buy order")

		return err
	}

	return nil
}

func (s *Arbitrage) prepareAmount(a *big.Float, pair *storage.Pair) string {
	amountStr := a.Text('f', pair.QuantityScale+1)
	amountArr := strings.Split(amountStr, ".")

	resultAmount := amountArr[0]

	if len(amountArr) == 2 && pair.QuantityScale != 0 {
		resultAmount = resultAmount + "." + amountArr[1][0:pair.QuantityScale]
	}

	return resultAmount
}
