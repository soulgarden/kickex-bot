package subscriber

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Pairs struct {
	cfg     *conf.Bot
	storage *storage.Storage
	logger  *zerolog.Logger
}

func NewPairs(cfg *conf.Bot, storage *storage.Storage, logger *zerolog.Logger) *Pairs {
	return &Pairs{cfg: cfg, storage: storage, logger: logger}
}

func (s *Pairs) Start(ctx context.Context, interrupt chan os.Signal) {
	s.logger.Warn().Msg("pairs subscriber starting...")

	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")
		interrupt <- syscall.SIGSTOP

		return
	}

	defer cli.Close()

	err = cli.GetPairsAndSubscribe()
	if err != nil {
		s.logger.Err(err).Msg("get pairs and subscribe")
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case msg, ok := <-cli.ReadCh:
			if !ok {
				s.logger.Err(dictionary.ErrWsReadChannelClosed).Msg("read channel closed")
				interrupt <- syscall.SIGSTOP

				return
			}

			s.logger.Debug().Bytes("payload", msg).Msg("got message")

			err := s.checkErrorResponse(msg)
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
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return errors.New(er.Error.Reason)
	}

	return nil
}
