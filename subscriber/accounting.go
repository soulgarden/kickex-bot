package subscriber

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mailru/easyjson"

	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/service"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Accounting struct {
	cfg            *conf.Bot
	storage        *storage.Storage
	wsEventBroker  *broker.Broker
	accEventBroker *broker.Broker
	wsSvc          *service.WS
	balanceSvc     *service.Balance
	logger         *zerolog.Logger
}

func NewAccounting(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	accEventBroker *broker.Broker,
	wsSvc *service.WS,
	balanceSvc *service.Balance,
	logger *zerolog.Logger,
) *Accounting {
	return &Accounting{
		cfg:            cfg,
		storage:        storage,
		wsEventBroker:  eventBroker,
		wsSvc:          wsSvc,
		balanceSvc:     balanceSvc,
		accEventBroker: accEventBroker,
		logger:         logger,
	}
}

func (s *Accounting) Start(ctx context.Context) error {
	s.logger.Warn().Msg("accounting subscriber starting...")
	defer s.logger.Warn().Msg("accounting subscriber stopped")

	eventsCh := s.wsEventBroker.Subscribe("accounting subscriber")
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.SubscribeAccounting(false)
	if err != nil {
		s.logger.Err(err).Msg("subscribe accounting")

		return err
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return dictionary.ErrEventChannelClosed
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

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

			s.logger.Debug().
				Bytes("payload", msg).
				Msg("got message")

			err = s.checkErrorResponse(msg)
			if err != nil {
				s.logger.Err(err).Msg("check error response")

				return err
			}

			r := &response.AccountingUpdates{}
			err = easyjson.Unmarshal(msg, r)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			for _, order := range r.Orders {
				o, err := storage.NewOrderByResponse(order)
				if err != nil {
					s.logger.Err(err).Msg("new order by response")

					return err
				}

				s.storage.UpsertUserOrder(o)

				pair := strings.Split(o.Pair, "/")

				if v := s.storage.GetOrderBook(pair[0], pair[1]); v != nil {
					v.OrderBookEventBroker.Publish(0)
				}
			}

			err = s.balanceSvc.UpdateStorageBalances(r.Balance)
			if err != nil {
				s.logger.Err(err).Msg("update storage balances")

				return err
			}

			s.storage.AppendDeals(r.Deals...)
			s.accEventBroker.Publish(true)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Accounting) checkErrorResponse(msg []byte) error {
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
