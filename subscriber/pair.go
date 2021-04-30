package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Pairs struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *broker.Broker
	wsSvc       *service.WS
	logger      *zerolog.Logger
}

func NewPairs(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	wsSvc *service.WS,
	logger *zerolog.Logger,
) *Pairs {
	return &Pairs{cfg: cfg, storage: storage, eventBroker: eventBroker, wsSvc: wsSvc, logger: logger}
}

func (s *Pairs) Start(ctx context.Context, interrupt chan os.Signal) {
	s.logger.Warn().Msg("pairs subscriber starting...")
	defer s.logger.Warn().Msg("pairs subscriber stopped")

	eventsCh := s.eventBroker.Subscribe()
	defer s.eventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.GetPairsAndSubscribe()
	if err != nil {
		s.logger.Err(err).Msg("get pairs and subscribe")
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				interrupt <- syscall.SIGSTOP

				return
			}

			msg, ok := e.([]byte)
			if !ok {
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

			if strconv.FormatInt(id, 10) != rid.ID {
				continue
			}

			err = s.checkErrorResponse(msg)
			if err != nil {
				interrupt <- syscall.SIGSTOP

				return
			}

			r := &response.Pairs{}

			err = json.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")
				interrupt <- syscall.SIGSTOP

				return
			}

			s.storage.UpdatePairs(r.Pairs)

		case <-ctx.Done():
			interrupt <- syscall.SIGSTOP

			return
		}
	}
}

func (s *Pairs) checkErrorResponse(msg []byte) error {
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
