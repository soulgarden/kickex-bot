package subscriber

import (
	"context"
	"encoding/json"
	"os"
	"syscall"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Accounting struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *Broker
	logger      *zerolog.Logger
}

func NewAccounting(cfg *conf.Bot, storage *storage.Storage, eventBroker *Broker, logger *zerolog.Logger) *Accounting {
	return &Accounting{cfg: cfg, storage: storage, eventBroker: eventBroker, logger: logger}
}

func (s *Accounting) Start(ctx context.Context, interrupt chan os.Signal) error {
	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")

		return err
	}

	defer cli.Close()

	err = cli.SubscribeAccounting(true)
	if err != nil {
		s.logger.Err(err).Msg("subscribe accounting")

		return err
	}

	for {
		select {
		case msg, ok := <-cli.ReadCh:
			if !ok {
				return dictionary.ErrWsReadChannelClosed
			}

			s.logger.Debug().
				Bytes("payload", msg).
				Msg("got message")

			err = s.checkErrorResponse(msg)
			if err != nil {
				return err
			}

			r := &response.AccountingUpdates{}
			err := json.Unmarshal(msg, r)

			if err != nil {
				s.logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			for _, order := range r.Orders {
				s.storage.UserOrders[order.ID] = order
			}

			for _, balance := range r.Balance {
				s.storage.Balances[balance.CurrencyCode] = balance
			}

			s.storage.Deals = append(s.storage.Deals, r.Deals...)

			s.eventBroker.Publish(0)
		case <-ctx.Done():
			interrupt <- syscall.SIGSTOP

			return nil
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
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return err
	}

	return nil
}
