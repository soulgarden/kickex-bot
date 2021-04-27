package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Order struct {
	cfg     *conf.Bot
	storage *storage.Storage
	logger  *zerolog.Logger
}

func NewOrder(cfg *conf.Bot, storage *storage.Storage, logger *zerolog.Logger) *Order {
	return &Order{cfg: cfg, storage: storage, logger: logger}
}

func (s *Order) UpdateOrderStates(ctx context.Context, interrupt chan os.Signal) error {
	cli, err := client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")
		interrupt <- syscall.SIGSTOP

		return err
	}

	defer cli.Close()

	err = cli.Auth()
	if err != nil {
		interrupt <- syscall.SIGSTOP

		return err
	}

	oNumber := len(s.storage.UserOrders)
	oNumberProcessed := 0

	for _, o := range s.storage.UserOrders {
		err := cli.GetOrder(o.ID)
		if err != nil {
			s.logger.Fatal().Err(err).Msg("get order")
		}
	}

	for {
		select {
		case msg, ok := <-cli.ReadCh:
			if !ok {
				s.logger.Err(err).Msg("read channel closed")
				interrupt <- syscall.SIGSTOP

				return err
			}

			err := s.checkErrorResponse(msg)
			if err != nil {
				interrupt <- syscall.SIGSTOP

				return err
			}

			r := &response.GetOrder{}

			err = json.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")
				interrupt <- syscall.SIGSTOP

				return err
			}

			if r.Order != nil {
				s.storage.SetUserOrder(r.Order)
				s.logger.Warn().Int64("oid", r.Order.ID).Msg("order state updated")
			}

			oNumberProcessed++

			if oNumberProcessed == oNumber {
				return nil
			}

		case <-ctx.Done():
			return nil
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
			s.logger.Warn().Bytes("response", msg).Msg("received order not found")

			return nil
		}

		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return errors.New(er.Error.Reason)
	}

	return nil
}
