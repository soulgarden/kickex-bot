package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/mailru/easyjson"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

const cancelOrderTimeout = time.Second * 10

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

func (s *Order) UpdateOrdersStates(ctx context.Context, interrupt chan<- os.Signal) error {
	s.logger.Warn().Msg("order states updater starting...")
	defer s.logger.Warn().Msg("order states updater stopped")

	eventsCh := s.wsEventBroker.Subscribe("update order states")
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

			interrupt <- syscall.SIGINT

			return err
		}

		reqIds[strconv.FormatInt(id, dictionary.DefaultIntBase)] = true
	}

	oNumber := len(reqIds)

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGINT

				return nil
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGINT

				return dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}

			err := easyjson.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return err
			}

			if _, ok := reqIds[rid.ID]; !ok {
				continue
			}

			_, err = s.processOrderMsg(msg)
			if err != nil {
				s.logger.Err(err).Msg("process order msg")

				interrupt <- syscall.SIGINT

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

func (s *Order) UpdateOrderState(ctx context.Context, interrupt chan<- os.Signal, rid int64) (*storage.Order, error) {
	eventsCh := s.wsEventBroker.Subscribe("update order state")
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGINT

				return nil, nil
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGINT

				return nil, dictionary.ErrCantConvertInterfaceToBytes
			}

			resp := &response.ID{}

			err := easyjson.Unmarshal(msg, resp)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return nil, nil
			}

			if strconv.FormatInt(rid, dictionary.ExtendedPrecision) != resp.ID {
				continue
			}

			o, err := s.processOrderMsg(msg)
			if err != nil {
				s.logger.Err(err).Msg("process order msg")

				interrupt <- syscall.SIGINT

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

	err := easyjson.Unmarshal(msg, er)
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
	err := s.checkErrorResponse(msg)
	if err != nil {
		return nil, err
	}

	r := &response.GetOrder{}

	err = easyjson.Unmarshal(msg, r)
	if err != nil {
		s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

		return nil, err
	}

	if r.Order == nil {
		return nil, nil
	}

	o, err := storage.NewOrderByResponse(r.Order)
	if err != nil {
		s.logger.Err(err).Msg("new order by response")

		return nil, err
	}

	s.storage.UpsertUserOrder(o)
	s.logger.Warn().Int64("oid", r.Order.ID).Msg("order state updated")

	return o, nil
}

func (s *Order) CancelOrder(orderID int64) error {
	eventsCh := s.wsEventBroker.Subscribe("cancel order")
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.CancelOrder(orderID)
	if err != nil {
		s.logger.Fatal().Int64("oid", orderID).Msg("cancel order")
	}

	for {
		select {
		case e := <-eventsCh:
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

			if strconv.FormatInt(id, dictionary.DefaultIntBase) != rid.ID {
				continue
			}

			s.logger.Info().
				Int64("oid", orderID).
				Bytes("payload", msg).
				Msg("cancel order response received")

			er := &response.Error{}

			err = easyjson.Unmarshal(msg, er)
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

		case <-time.After(cancelOrderTimeout):
			s.logger.Err(err).Msg(err.Error())

			return dictionary.ErrCancelOrderTimeout
		}
	}
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
