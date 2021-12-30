package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

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

func (s *Accounting) Start(ctx context.Context, interrupt chan os.Signal, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	s.logger.Warn().Msg("accounting subscriber starting...")
	defer s.logger.Warn().Msg("accounting subscriber stopped")

	eventsCh := s.wsEventBroker.Subscribe()
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.SubscribeAccounting(false)
	if err != nil {
		s.logger.Err(err).Msg("subscribe accounting")
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGSTOP

				return
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGSTOP

				return
			}

			rid := &response.ID{}

			err := json.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return
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

				interrupt <- syscall.SIGSTOP

				return
			}

			r := &response.AccountingUpdates{}
			err = json.Unmarshal(msg, r)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return
			}

			for _, order := range r.Orders {
				o, err := storage.NewOrderByResponse(order)
				if err != nil {
					s.logger.Err(err).Msg("new order by response")

					interrupt <- syscall.SIGSTOP

					return
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

				interrupt <- syscall.SIGSTOP

				return
			}

			s.storage.AppendDeals(r.Deals...)
			s.accEventBroker.Publish(true)
		case <-ctx.Done():
			interrupt <- syscall.SIGSTOP

			return
		}
	}
}

func (s *Accounting) checkErrorResponse(msg []byte) error {
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
