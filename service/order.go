package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Order struct {
	cfg           *conf.Bot
	storage       *storage.Storage
	wsEventBroker *broker.Broker
	wsSvc         *WS
	logger        *zerolog.Logger
}

func NewOrder(
	cfg *conf.Bot,
	storage *storage.Storage,
	wsEventBroker *broker.Broker,
	wsSvc *WS,
	logger *zerolog.Logger,
) *Order {
	return &Order{cfg: cfg, storage: storage, wsEventBroker: wsEventBroker, wsSvc: wsSvc, logger: logger}
}

func (s *Order) UpdateOrdersStates(ctx context.Context, interrupt chan os.Signal) error {
	s.logger.Warn().Msg("order states updater starting...")
	defer s.logger.Warn().Msg("order states updater stopped")

	eventsCh := s.wsEventBroker.Subscribe()
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	oNumberProcessed := 0

	reqIds := map[string]bool{}

	for _, o := range s.storage.GetUserOrders() {
		if o.State > dictionary.StateActive {
			continue
		}

		id, err := s.wsSvc.GetOrder(o.ID)
		if err != nil {
			s.logger.Err(err).Msg("get order")

			interrupt <- syscall.SIGSTOP

			return err
		}

		reqIds[strconv.FormatInt(id, 10)] = true
	}

	oNumber := len(reqIds)

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGSTOP

				return nil
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

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

			if _, ok := reqIds[rid.ID]; !ok {
				continue
			}

			_, err = s.processOrderMsg(msg)
			if err != nil {
				s.logger.Err(err).Msg("process order msg")

				interrupt <- syscall.SIGSTOP

				return err
			}

			oNumberProcessed++

			if oNumberProcessed == oNumber {
				return nil
			}

		case <-ctx.Done():
			return nil
		case <-time.After(time.Minute):
			s.logger.Err(dictionary.ErrUpdateOrderStateTimeout).Msg("update order state timeout")

			return dictionary.ErrUpdateOrderStateTimeout
		}
	}
}

func (s *Order) UpdateOrderState(ctx context.Context, interrupt chan os.Signal, rid int64) (*storage.Order, error) {
	eventsCh := s.wsEventBroker.Subscribe()
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGSTOP

				return nil, nil
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGSTOP

				return nil, dictionary.ErrCantConvertInterfaceToBytes
			}

			resp := &response.ID{}

			err := json.Unmarshal(msg, resp)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return nil, nil
			}

			if strconv.FormatInt(rid, 10) != resp.ID {
				continue
			}

			o, err := s.processOrderMsg(msg)
			if err != nil {
				s.logger.Err(err).Msg("process order msg")

				interrupt <- syscall.SIGSTOP

				return nil, err
			}

			return o, nil

		case <-ctx.Done():
			return nil, nil
		case <-time.After(time.Minute):
			s.logger.Err(dictionary.ErrUpdateOrderStateTimeout).Msg("update order state timeout")

			return nil, dictionary.ErrUpdateOrderStateTimeout
		}
	}
}

func (s *Order) checkErrorResponse(msg []byte) error {
	er := &response.Error{}

	err := json.Unmarshal(msg, er)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return err
	}

	if er.Error != nil {
		if er.Error.Code == response.OrderNotFoundOrOutdated {
			s.logger.Err(dictionary.ErrOrderNotFoundOrOutdated).Bytes("response", msg).Msg("received order not found")

			return dictionary.ErrOrderNotFoundOrOutdated
		}

		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return err
	}

	return nil
}

func (s *Order) processOrderMsg(msg []byte) (*storage.Order, error) {
	var ok bool

	err := s.checkErrorResponse(msg)
	if err != nil {
		return nil, err
	}

	r := &response.GetOrder{}

	err = json.Unmarshal(msg, r)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return nil, err
	}

	if r.Order == nil {
		return nil, nil
	}

	createdTS, err := strconv.ParseInt(r.Order.CreatedTimestamp, 10, 0)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("parse string as int64")

		return nil, err
	}

	o := &storage.Order{
		ID:               r.Order.ID,
		TradeTimestamp:   r.Order.TradeTimestamp,
		CreatedTimestamp: time.Unix(0, createdTS),
		State:            r.Order.State,
		Modifier:         r.Order.Modifier,
		Pair:             r.Order.Pair,
		TradeIntent:      r.Order.TradeIntent,
		TotalFeeQuoted:   r.Order.TotalFeeQuoted,
		TotalFeeExt:      r.Order.TotalFeeExt,
		Activated:        r.Order.Activated,
		TpActivateLevel:  r.Order.TpActivateLevel,
		TrailDistance:    r.Order.TrailDistance,
		TpSubmitLevel:    r.Order.TpSubmitLevel,
		TpLimitPrice:     r.Order.LimitPrice,
		SlSubmitLevel:    r.Order.SlSubmitLevel,
		SlLimitPrice:     r.Order.SlLimitPrice,
		StopTimestamp:    r.Order.StopTimestamp,
		TriggeredSide:    r.Order.TriggeredSide,
	}

	o.OrderedVolume, ok = big.NewFloat(0).SetString(r.Order.OrderedVolume)
	if !ok {
		s.logger.Err(dictionary.ErrParseFloat).
			Str("val", r.Order.OrderedVolume).
			Msg("parse string as float")

		return nil, err
	}

	o.LimitPrice, ok = big.NewFloat(0).SetString(r.Order.LimitPrice)
	if !ok {
		s.logger.Err(dictionary.ErrParseFloat).
			Str("val", r.Order.LimitPrice).
			Msg("parse string as float")

		return nil, err
	}

	o.TotalSellVolume = big.NewFloat(0)
	if r.Order.TotalSellVolume != "" {
		o.TotalSellVolume, ok = big.NewFloat(0).SetString(r.Order.TotalSellVolume)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Str("val", r.Order.TotalSellVolume).
				Msg("parse string as float")

			return nil, err
		}
	}

	o.TotalBuyVolume = big.NewFloat(0)
	if r.Order.TotalBuyVolume != "" {
		o.TotalBuyVolume, ok = big.NewFloat(0).SetString(r.Order.TotalBuyVolume)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Str("val", r.Order.TotalBuyVolume).
				Msg("parse string as float")

			return nil, err
		}
	}

	s.storage.UpsertUserOrder(o)
	s.logger.Warn().Int64("oid", r.Order.ID).Msg("order state updated")

	return o, nil
}

func (s *Order) CancelOrder(orderID int64) error {
	eventsCh := s.wsEventBroker.Subscribe()
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.CancelOrder(orderID)
	if err != nil {
		s.logger.Fatal().Int64("oid", orderID).Msg("cancel order")
	}

	// TODO: add timeout
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

func (s *Order) SendCancelOrderRequest(orderID int64) {
	_, err := s.wsSvc.CancelOrder(orderID)
	if err != nil {
		s.logger.Fatal().
			Int64("oid", orderID).
			Msg("send cancel order request")
	}

	s.logger.Warn().
		Int64("oid", orderID).
		Msg("send cancel order request")
}
