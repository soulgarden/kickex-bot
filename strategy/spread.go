package strategy

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mailru/easyjson"

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
	pair          *storage.Pair
	orderBook     *storage.Book
	storage       *storage.Storage
	wsEventBroker *broker.Broker
	conversion    *service.Conversion
	wsSvc         *service.WS
	orderSvc      *service.Order

	watchSvc       *spreadSvc.Watch
	deciderSvc     *spreadSvc.Decider
	spreadOrderSvc *spreadSvc.Order
	tgSvc          *spreadSvc.Tg

	totalBuyInUSDT *big.Float

	forceCheckBroker *broker.Broker

	logger *zerolog.Logger
}

func NewSpread(
	cfg *conf.Bot,
	storage *storage.Storage,
	wsEventBroker *broker.Broker,
	conversion *service.Conversion,
	wsSvc *service.WS,
	pair *storage.Pair,
	orderBook *storage.Book,
	orderSvc *service.Order,
	watchSvc *spreadSvc.Watch,
	deciderSvc *spreadSvc.Decider,
	spreadOrderSvc *spreadSvc.Order,
	tgSvc *spreadSvc.Tg,
	forceCheckBroker *broker.Broker,
	logger *zerolog.Logger,
) (*Spread, error) {
	totalBuyInUSDT, ok := big.NewFloat(0).SetString(cfg.Spread.TotalBuyInUSDT)
	if !ok {
		logger.Err(dictionary.ErrParseFloat).Str("val", cfg.Spread.TotalBuyInUSDT).Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	return &Spread{
		pair:             pair,
		storage:          storage,
		wsEventBroker:    wsEventBroker,
		conversion:       conversion,
		wsSvc:            wsSvc,
		totalBuyInUSDT:   totalBuyInUSDT,
		orderBook:        orderBook,
		orderSvc:         orderSvc,
		watchSvc:         watchSvc,
		deciderSvc:       deciderSvc,
		spreadOrderSvc:   spreadOrderSvc,
		tgSvc:            tgSvc,
		forceCheckBroker: forceCheckBroker,
		logger:           logger,
	}, nil
}

func (s *Spread) Start(ctx context.Context, g *errgroup.Group) error {
	s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("spread manager starting...")
	defer s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("spread manager stopped")

	oldSessionChan := make(chan int, 1)

	if len(s.orderBook.SpreadSessions) > 0 {
		oldSessionChan <- 0
	}

	ticker := time.NewTicker(sessCreationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-oldSessionChan:
			oldG, oldCtx := errgroup.WithContext(ctx)

			for _, sess := range s.orderBook.SpreadSessions {
				if !sess.GetIsDone() {
					sess := sess

					oldG.Go(func() error {
						return s.processOldSession(oldCtx, oldG, sess)
					})
				}
			}

			s.logger.Info().Str("pair", s.pair.GetPairName()).Msg("wait for finishing old sessions")

			err := oldG.Wait()
			s.logger.Err(err).Str("pair", s.pair.GetPairName()).Msg("old sessions finished")

		case <-ctx.Done():
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders starting")
			s.cleanUpBeforeShutdown()
			s.logger.Warn().Str("pair", s.pair.GetPairName()).Msg("cleanup active orders finished")

			close(oldSessionChan)

			return nil
		case <-ticker.C:
			if s.orderBook.SpreadActiveSessionID.Load() != "" {
				continue
			}

			if err := s.CreateSession(ctx, g); err != nil {
				return err
			}
		}
	}
}

func (s *Spread) CreateSession(ctx context.Context, g *errgroup.Group) error {
	volume, err := s.getStartBuyVolume()
	if err != nil {
		s.logger.Err(err).Msg("get start buy volume")

		return err
	}

	sessCtx, cancel := context.WithCancel(ctx)

	defer cancel()

	sess := s.orderBook.NewSpreadSession(volume)

	s.logger.Warn().Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("start session")

	err = s.processSession(sessCtx, g, sess)
	s.logger.Err(err).Str("id", sess.ID).Str("pair", s.pair.GetPairName()).Msg("session finished")

	if err != nil {
		return err
	}

	if s.orderBook.SpreadActiveSessionID.Load() == sess.ID {
		s.orderBook.SpreadActiveSessionID.Store("")
	}

	return nil
}

func (s *Spread) processOldSession(ctx context.Context, g *errgroup.Group, sess *storageSpread.Session) error {
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

	sessCtx, cancel := context.WithCancel(ctx)

	defer cancel()

	switch {
	case sess.GetActiveSellOrderID() != 0:
		order := s.storage.GetUserOrder(sess.GetActiveSellOrderID())
		if order == nil {
			err := s.spreadOrderSvc.UpdateOrderStateByID(ctx, sess.GetActiveSellOrderID())
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveSellOrderID()).
					Msg("update sell order state by id")

				return err
			}
		}

		g.Go(func() error { return s.watchSvc.Start(ctx, sess, sess.GetActiveSellOrderID()) })
	case sess.GetActiveBuyOrderID() != 0:
		order := s.storage.GetUserOrder(sess.GetActiveBuyOrderID())
		if order == nil {
			err := s.spreadOrderSvc.UpdateOrderStateByID(ctx, sess.GetActiveBuyOrderID())
			if err != nil {
				s.logger.Err(err).
					Str("id", sess.ID).
					Int64("oid", sess.GetActiveBuyOrderID()).
					Msg("update buy order state by id")

				return err
			}
		}

		g.Go(func() error { return s.watchSvc.Start(ctx, sess, sess.GetActiveBuyOrderID()) })
	case sess.GetActiveBuyExtOrderID() != "":
		o, err := s.updateOrderStateByExtID(ctx, sess.GetActiveBuyExtOrderID())
		if err != nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveBuyExtOrderID()).
				Int64("oid", sess.GetActiveBuyOrderID()).
				Msg("update buy order state by ext id")

			return err
		}

		sess.SetActiveBuyOrderID(o.ID)
		sess.SetActiveBuyExtOrderID("")
		sess.SetActiveBuyOrderRequestID("")

		g.Go(func() error { return s.watchSvc.Start(ctx, sess, sess.GetActiveBuyOrderID()) })
	case sess.GetActiveSellExtOrderID() != "":
		o, err := s.updateOrderStateByExtID(ctx, sess.GetActiveSellExtOrderID())
		if err != nil {
			s.logger.Err(err).
				Str("id", sess.ID).
				Str("ext oid", sess.GetActiveSellExtOrderID()).
				Int64("oid", sess.GetActiveSellOrderID()).
				Msg("update sell order state by ext id")

			return err
		}

		sess.SetActiveSellOrderID(o.ID)
		sess.SetActiveSellExtOrderID("")
		sess.SetActiveSellOrderRequestID("")

		g.Go(func() error { return s.watchSvc.Start(ctx, sess, sess.GetActiveSellOrderID()) })
	}

	s.tgSvc.OldSessionStarted(sess)

	err := s.processSession(sessCtx, g, sess)
	s.logger.Err(err).
		Str("id", sess.ID).
		Str("pair", s.pair.GetPairName()).
		Msg("session finished")

	return err
}

func (s *Spread) processSession(
	ctx context.Context,
	g *errgroup.Group,
	sess *storageSpread.Session,
) error {
	g.Go(func() error {
		if err := s.listenNewOrders(ctx, g, sess); err != nil {
			s.logger.Err(err).Str("id", sess.ID).Msg("listen new orders")

			return err
		}

		return nil
	})

	err := s.deciderSvc.Start(ctx, sess)
	if err != nil {
		s.logger.Err(err).Msg("decide")
	}

	return err
}

func (s *Spread) listenNewOrders(
	ctx context.Context,
	g *errgroup.Group,
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

	eventsCh := s.wsEventBroker.Subscribe("listen new orders")
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
				return dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}

			err := easyjson.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			if sess.GetActiveBuyOrderRequestID() != rid.ID && sess.GetActiveSellOrderRequestID() != rid.ID {
				continue
			}

			s.logger.Debug().
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

			err = easyjson.Unmarshal(msg, co)
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

			g.Go(func() error { return s.watchSvc.Start(ctx, sess, co.OrderID) })

		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Spread) checkListenOrderErrors(msg []byte, sess *storageSpread.Session) (isSkipRequired bool, err error) {
	er := &response.Error{}

	err = easyjson.Unmarshal(msg, er)
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

				if err := s.spreadOrderSvc.SetBuyOrderExecutedFlags(
					sess,
					s.storage.GetUserOrder(sess.GetPrevBuyOrderID()),
				); err != nil {
					s.logger.Err(err).Msg("set buy order executed flags")

					return false, err
				}
			} else if er.ID == sess.GetActiveSellOrderRequestID() {
				s.logger.Warn().
					Str("id", sess.ID).
					Str("pair", s.pair.GetPairName()).
					Int64("prev oid", sess.GetPrevSellOrderID()).
					Str("ext oid", sess.GetActiveSellExtOrderID()).
					Msg("consider prev sell order as executed, allow to place buy order")

				if err := s.spreadOrderSvc.SetSellOrderExecutedFlags(
					sess,
					s.storage.GetUserOrder(sess.GetPrevSellOrderID()),
				); err != nil {
					s.logger.Err(err).Msg("set sell order executed flags")

					return false, err
				}
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

					sess.SetBuyOrderCancelledFlags()
					s.forceCheckBroker.Publish(struct{}{})

					return true, nil
				} else if o.State == dictionary.StateDone {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevBuyOrderID()).
						Str("ext oid", sess.GetActiveBuyExtOrderID()).
						Msg("altered buy order already done")

					if err := s.spreadOrderSvc.SetBuyOrderExecutedFlags(
						sess,
						s.storage.GetUserOrder(sess.GetPrevBuyOrderID()),
					); err != nil {
						s.logger.Err(err).Msg("set buy order executed flags")

						return false, err
					}

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

					sess.SetSellOrderCancelledFlags()
					s.forceCheckBroker.Publish(struct{}{})

					return true, nil
				} else if o.State == dictionary.StateDone {
					s.logger.Warn().
						Str("id", sess.ID).
						Str("pair", s.pair.GetPairName()).
						Int64("prev oid", sess.GetPrevSellOrderID()).
						Str("ext oid", sess.GetActiveSellExtOrderID()).
						Msg("altered sell order already done")

					if err := s.spreadOrderSvc.SetSellOrderExecutedFlags(
						sess,
						s.storage.GetUserOrder(sess.GetPrevSellOrderID()),
					); err != nil {
						s.logger.Err(err).Msg("set sell order executed flags")

						return false, err
					}

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

func (s *Spread) cleanUpBeforeShutdown() {
	for _, sess := range s.orderBook.SpreadSessions {
		if sess.GetIsDone() {
			continue
		}

		oid := sess.GetActiveBuyOrderID()

		if oid > 1 {
			if o := s.storage.GetUserOrder(oid); o != nil && o.State < dictionary.StateDone {
				err := s.orderSvc.SendCancelOrderRequest(oid)
				s.logger.Err(err).Int64("oid", oid).Msg("send cancel order request")
			}
		}

		oid = sess.GetActiveSellOrderID()

		if oid > 1 {
			if o := s.storage.GetUserOrder(oid); o != nil && o.State < dictionary.StateDone {
				err := s.orderSvc.SendCancelOrderRequest(oid)
				s.logger.Err(err).Int64("oid", oid).Msg("send cancel order request")
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

func (s *Spread) updateOrderStateByExtID(
	ctx context.Context,
	extID string,
) (*storage.Order, error) {
	rid, err := s.wsSvc.GetOrderByExtID(extID)
	if err != nil {
		s.logger.Err(err).
			Str("ext oid", extID).
			Msg("send get order by ext id request")

		return nil, err
	}

	o, err := s.orderSvc.UpdateOrderState(ctx, rid)
	if err != nil || o == nil {
		s.logger.Err(err).
			Str("ext oid", extID).
			Msg("get order by ext id failed")

		return nil, err
	}

	return o, nil
}
