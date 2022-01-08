package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	spreadSvc "github.com/soulgarden/kickex-bot/service/spread"

	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

const sendInterval = 300 * time.Second
const spreadForAlert = 2.7
const orderExecutionInterval = time.Minute * 5
const chSize = 1024

type Arbitrage struct {
	cfg            *conf.Bot
	storage        *storage.Storage
	conversion     *service.Conversion
	tgSvc          *service.Telegram
	wsSvc          *service.WS
	wsEventBroker  *broker.Broker
	accEventBroker *broker.Broker
	orderSvc       *service.Order
	sessSvc        *spreadSvc.Session
	balanceSvc     *service.Balance
	sentAt         *time.Time
	totalBuyInUSDT *big.Float
	logger         *zerolog.Logger
}

func NewArbitrage(
	cfg *conf.Bot,
	storage *storage.Storage,
	conversion *service.Conversion,
	tgSvc *service.Telegram,
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
		tgSvc:          tgSvc,
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

func (s *Arbitrage) Start(ctx context.Context, wg *sync.WaitGroup, interrupt chan<- os.Signal) {
	defer wg.Done()

	s.logger.Warn().Msg("arbitrage process started")
	defer s.logger.Warn().Msg("stop arbitrage process")

	ch := make(chan bool, chSize)

	go s.collectEvents(ctx, interrupt, ch)

	pair := s.storage.GetPair(s.cfg.Arbitrage.Pair)

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGINT

				return
			}

			s.check(ctx, pair)

		case <-ctx.Done():
			return
		}
	}
}

func (s *Arbitrage) collectEvents(ctx context.Context, interrupt chan<- os.Signal, ch chan<- bool) {
	var wg sync.WaitGroup

	s.logger.Warn().Msg("start collect events")
	defer s.logger.Warn().Msg("finish collect events")

	pairs := s.GetPairsList()

	for pairName := range pairs {
		pair := s.storage.GetPair(pairName)

		wg.Add(1)

		go s.listenPairEvents(ctx, &wg, interrupt, pair, ch)
	}

	wg.Wait()
}

func (s *Arbitrage) listenPairEvents(
	ctx context.Context,
	wg *sync.WaitGroup,
	interrupt chan<- os.Signal,
	pair *storage.Pair,
	ch chan<- bool,
) {
	defer wg.Done()

	s.logger.Warn().Str("pair", pair.GetPairName()).Msg("start listen pair events")
	defer s.logger.Warn().Str("pair", pair.GetPairName()).Msg("finish listen pair events")

	e := s.storage.GetOrderBook(pair.BaseCurrency, pair.QuoteCurrency).OrderBookEventBroker.Subscribe("listen events")
	defer s.storage.GetOrderBook(pair.BaseCurrency, pair.QuoteCurrency).OrderBookEventBroker.Unsubscribe(e)

	for {
		select {
		case _, ok := <-e:
			if !ok {
				s.logger.Warn().Msg("receive event error")

				interrupt <- syscall.SIGINT

				return
			}

			ch <- true
		case <-ctx.Done():
			return
		}
	}
}

func (s *Arbitrage) check(ctx context.Context, pair *storage.Pair) {
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
		return
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
		s.logger.Fatal().Err(err).Msg("check buy base option finished with error")
		s.tgSvc.Send(
			fmt.Sprintf(
				`env: %s,
pair %s,
check buy base option finished with error,
error %s`,
				s.cfg.Env,
				baseQuotedPair.GetPairName(),
				err.Error(),
			),
		)
	}

	s.checkBuyQuotedOptions(
		baseUSDTPair,
		quotedBuyOrder,
		quotedUSDTPair,
		baseSellOrder,
		baseQuotedPair,
		baseQuotedBuyOrder,
	)
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

	// option 1 // buy base for usdt / sell base for quoted / sell quoted for usdt
	if baseBuyOrder.Total.Cmp(startBuyVolume) == 1 &&
		baseQuotedSellOrder.Total.Cmp(baseQuotedPair.MinVolume) == 1 &&
		quotedSellOrder.Total.Cmp(quotedUSDTPair.MinVolume) == 1 {
		baseBuyOrderAmount := big.NewFloat(0).Quo(startBuyVolume, baseBuyOrder.Price)

		s.logger.Debug().
			Str("pair", baseUSDTPair.GetPairName()).
			Str("total", startBuyVolume.Text('f', baseUSDTPair.VolumeScale)).
			Str("price", baseBuyOrder.Price.Text('f', baseUSDTPair.PriceScale)).
			Str("amount", baseBuyOrderAmount.Text('f', baseUSDTPair.QuantityScale)).
			Msg("buy base")

		baseQuotedSellOrderTotal := big.NewFloat(0).Mul(baseBuyOrderAmount, baseQuotedSellOrder.Price)

		s.logger.Debug().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("amount", baseBuyOrderAmount.Text('f', baseUSDTPair.QuantityScale)).
			Str("price", baseQuotedSellOrder.Price.Text('f', baseQuotedPair.PriceScale)).
			Str("total", baseQuotedSellOrderTotal.Text('f', baseQuotedPair.VolumeScale)).
			Msg("sell base for quoted")

		quotedSellOrderUSDTAmount := big.NewFloat(0).Mul(baseQuotedSellOrderTotal, quotedSellOrder.Price)

		s.logger.Debug().
			Str("pair", quotedUSDTPair.GetPairName()).
			Str("amount", baseQuotedSellOrderTotal.Text('f', dictionary.ExtendedPrecision)).
			Str("price", quotedSellOrder.Price.Text('f', baseUSDTPair.PriceScale)).
			Str("total", quotedSellOrderUSDTAmount.Text('f', quotedUSDTPair.VolumeScale)).
			Msg("sell quoted")

		// (x * 100 / y) - 100
		spread := big.NewFloat(0).Sub(
			big.NewFloat(0).
				Quo(big.NewFloat(0).Mul(quotedSellOrderUSDTAmount, dictionary.MaxPercentFloat), startBuyVolume),
			dictionary.MaxPercentFloat,
		)

		s.logger.Info().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("before usdt amount", startBuyVolume.Text('f', dictionary.ExtendedPrecision)).
			Str("after usdt amount", quotedSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision)).
			Str("spread", spread.Text('f', dictionary.DefaultPrecision)).
			Msg("pair spread")

		if spread.Cmp(big.NewFloat(spreadForAlert)) == 1 {
			now := time.Now()
			if s.sentAt == nil || time.Now().After(s.sentAt.Add(sendInterval)) {
				s.sentAt = &now

				s.tgSvc.Send(
					fmt.Sprintf(
						`env: %s,
pair %s,
arbitrage available,
before usdt amount %s,
after usdt amount %s,
spread %s`,
						s.cfg.Env,
						baseQuotedPair.GetPairName(),
						startBuyVolume.Text('f', dictionary.ExtendedPrecision),
						quotedSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision),
						spread.Text('f', dictionary.DefaultPrecision),
					),
				)
			}

			// 1. buy base for usdt
			s.logger.Warn().
				Str("pair", baseUSDTPair.GetPairName()).
				Str("amount", s.prepareAmount(baseBuyOrderAmount, baseUSDTPair)).
				Str("price", baseBuyOrder.Price.Text('f', baseUSDTPair.PriceScale)).
				Str("total", startBuyVolume.Text('f', baseUSDTPair.QuantityScale)).
				Msg("buy base")

			oid, err := s.createOrder(
				ctx,
				baseUSDTPair,
				baseBuyOrderAmount,
				baseBuyOrder.Price,
				dictionary.BuyBase,
			)
			s.logger.Err(err).Str("pair", baseUSDTPair.GetPairName()).Int64("oid", oid).Msg("create order")

			if err != nil {
				if errors.Is(err, dictionary.ErrOrderNotCompleted) {
					return s.cancelOrder(oid, baseUSDTPair)
				}

				return err
			}

			// 2. sell base for quoted
			o := s.storage.GetUserOrder(oid)
			buyBaseOrder := o

			if o == nil {
				s.logger.
					Err(dictionary.ErrOrderNotFoundOrOutdated).
					Int64("oid", oid).
					Msg("order not found in order book")

				return dictionary.ErrOrderNotFoundOrOutdated
			}

			s.logger.Warn().
				Str("pair", baseQuotedPair.GetPairName()).
				Str("amount", s.prepareAmount(o.TotalBuyVolume, baseQuotedPair)).
				Str("price", baseQuotedSellOrder.Price.Text('f', baseQuotedPair.PriceScale)).
				Str("total", baseQuotedSellOrderTotal.Text('f', baseQuotedPair.VolumeScale)).
				Msg("sell base for quoted")

			oid, err = s.createOrder(
				ctx,
				baseQuotedPair,
				o.TotalBuyVolume,
				baseQuotedSellOrder.Price,
				dictionary.SellBase,
			)
			s.logger.Err(err).Str("pair", baseQuotedPair.GetPairName()).Int64("oid", oid).Msg("create order")

			if err != nil {
				if errors.Is(err, dictionary.ErrOrderNotCompleted) {
					s.sendTGFailed(s.cfg.Env, dictionary.SecondStep, oid, baseQuotedPair.GetPairName())

					return s.cancelOrder(oid, baseQuotedPair)
				}

				return err
			}

			// 3. sell base for quoted
			o = s.storage.GetUserOrder(oid)
			if o == nil {
				s.logger.
					Err(dictionary.ErrOrderNotFoundOrOutdated).
					Int64("oid", oid).
					Msg("order not found in order book")

				return dictionary.ErrOrderNotFoundOrOutdated
			}

			sellVolume := big.NewFloat(0).Sub(o.TotalBuyVolume, o.TotalFeeQuoted)

			s.logger.Warn().
				Str("pair", quotedUSDTPair.GetPairName()).
				Str("amount", s.prepareAmount(sellVolume, quotedUSDTPair)).
				Str("price", quotedSellOrder.Price.Text('f', baseUSDTPair.PriceScale)).
				Str("total", quotedSellOrderUSDTAmount.Text('f', quotedUSDTPair.VolumeScale)).
				Msg("sell quoted")

			oid, err = s.createOrder(
				ctx,
				quotedUSDTPair,
				sellVolume,
				quotedSellOrder.Price,
				dictionary.SellBase,
			)
			s.logger.Err(err).Str("pair", baseQuotedPair.GetPairName()).Int64("oid", oid).Msg("create order")

			if err != nil {
				if errors.Is(err, dictionary.ErrOrderNotCompleted) {
					s.sendTGFailed(s.cfg.Env, dictionary.ThirdStepStep, oid, baseQuotedPair.GetPairName())

					return s.cancelOrder(oid, baseQuotedPair)
				}

				return err
			}

			o = s.storage.GetUserOrder(oid)
			if o == nil {
				s.logger.
					Err(dictionary.ErrOrderNotFoundOrOutdated).
					Int64("oid", oid).
					Msg("order not found in order book")

				return dictionary.ErrOrderNotFoundOrOutdated
			}

			s.sendTGSuccess(s.cfg.Env, baseQuotedPair.GetPairName(), big.NewFloat(0).
				Add(buyBaseOrder.TotalSellVolume, buyBaseOrder.TotalFeeQuoted).
				Text('f', baseUSDTPair.VolumeScale), sellVolume.Text('f', baseUSDTPair.VolumeScale))
		}
	}

	return nil
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

	// option 2 // buy quoted for usdt / buy base for quoted / sell base for usdt
	if quotedBuyOrder.Total.Cmp(quotedUSDTPair.MinVolume) == 1 &&
		baseQuotedBuyOrder.Total.Cmp(baseQuotedPair.MinVolume) == 0 &&
		baseSellOrder.Total.Cmp(baseUSDTPair.MinVolume) == 0 {
		quotedBuyOrderAmount := big.NewFloat(0).Quo(quotedUSDTPair.MinVolume, quotedBuyOrder.Price)

		s.logger.Debug().
			Str("pair", quotedUSDTPair.GetPairName()).
			Str("amount", quotedUSDTPair.MinVolume.Text('f', dictionary.ExtendedPrecision)).
			Str("price", quotedBuyOrder.Price.Text('f', quotedUSDTPair.PriceScale)).
			Str("total", quotedBuyOrderAmount.Text('f', dictionary.ExtendedPrecision)).
			Msg("buy quoted")

		baseQuotedBuyOrderTotal := big.NewFloat(0).Mul(quotedBuyOrderAmount, baseQuotedBuyOrder.Price)

		s.logger.Debug().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("amount", baseQuotedBuyOrderTotal.Text('f', dictionary.ExtendedPrecision)).
			Str("price", baseQuotedBuyOrder.Price.Text('f', baseQuotedPair.PriceScale)).
			Str("total", baseQuotedPair.MinVolume.Text('f', dictionary.ExtendedPrecision)).
			Msg("buy base for quoted")

		baseSellOrderUSDTAmount := big.NewFloat(0).Mul(baseQuotedBuyOrderTotal, baseSellOrder.Price)

		s.logger.Debug().
			Str("pair", baseUSDTPair.GetPairName()).
			Str("amount", baseQuotedBuyOrderTotal.Text('f', dictionary.ExtendedPrecision)).
			Str("price", baseSellOrder.Price.Text('f', baseUSDTPair.PriceScale)).
			Str("total", baseSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision)).
			Msg("sell quoted")

		// (x * 100 / y) - 100
		spread := big.NewFloat(0).Sub(
			big.NewFloat(0).
				Quo(big.NewFloat(0).Mul(baseSellOrderUSDTAmount, dictionary.MaxPercentFloat), quotedUSDTPair.MinVolume),
			dictionary.MaxPercentFloat,
		)

		s.logger.Info().
			Str("pair", baseQuotedPair.GetPairName()).
			Str("before usdt amount", quotedUSDTPair.MinVolume.Text('f', dictionary.ExtendedPrecision)).
			Str("after usdt amount", baseSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision)).
			Str("spread", spread.Text('f', dictionary.DefaultPrecision)).
			Msg("pair spread")

		if spread.Cmp(big.NewFloat(spreadForAlert)) == 1 {
			now := time.Now()
			if s.sentAt == nil || time.Now().After(s.sentAt.Add(sendInterval)) {
				s.sentAt = &now

				s.tgSvc.Send(
					fmt.Sprintf(
						`env: %s,
arbitrage available, but not supported
pair %s,
before usdt amount %s,
after usdt amount %s,
spread %s`,
						s.cfg.Env,
						baseQuotedPair.GetPairName(),
						quotedUSDTPair.MinVolume.Text('f', dictionary.ExtendedPrecision),
						baseSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision),
						spread.Text('f', dictionary.DefaultPrecision),
					))
			}
		}
	}
}

func (s *Arbitrage) createOrder(
	ctx context.Context,
	pair *storage.Pair,
	amount, price *big.Float,
	intent int,
) (int64, error) {
	oid, err := s.sendCreateOrderRequest(ctx, pair, amount, price, intent)
	s.logger.Err(err).Msg("send create order request")

	if err != nil {
		return 0, err
	}

	isCompleted, err := s.watchOrder(ctx, oid, pair)
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

	err := json.Unmarshal(msg, er)
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
			err := json.Unmarshal(msg, rid)

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

			err = json.Unmarshal(msg, co)
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
			// order sent, wait creation
			order := s.storage.GetUserOrder(orderID)
			if order == nil {
				s.logger.Warn().Int64("oid", orderID).Msg("order not found")

				if startedTime.Add(orderCreationDuration).Before(time.Now()) {
					s.logger.Err(dictionary.ErrOrderCreationEventNotReceived).Msg("order creation event not received")

					s.tgSvc.Send(fmt.Sprintf(
						`env: %s,
order creation event not received,
id: %d`,
						s.cfg.Env,
						orderID,
					))

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
		case <-time.After(orderExecutionInterval):
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

func (s *Arbitrage) sendTGSuccess(env, pairName, beforeUSDTAmount, afterUSDTAmount string) {
	s.tgSvc.Send(
		fmt.Sprintf(
			`env: %s,
			arbitrage done,
			pair %s,
			before usdt amount %s,
			after usdt amount %s`,
			env,
			pairName,
			beforeUSDTAmount,
			afterUSDTAmount,
		))
}

func (s *Arbitrage) sendTGFailed(env string, step int, oid int64, pairName string) {
	s.tgSvc.Send(
		fmt.Sprintf(
			`env: %s,
			arbitrage failed on %d step,
			cancel order %d,
			pair %s`,
			env,
			step,
			oid,
			pairName,
		))
}
