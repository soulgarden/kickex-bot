package subscriber

import (
	"context"
	"encoding/json"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

func SubscribeAccounting(
	ctx context.Context,
	cfg *conf.Bot,
	storage *storage.Storage,
	eventCh chan<- int,
	logger *zerolog.Logger,
) error {
	cli, err := client.NewWsCli(cfg, logger)
	if err != nil {
		logger.Err(err).Msg("connection error")

		return err
	}

	defer cli.Close()

	err = cli.SubscribeAccounting(false)
	if err != nil {
		logger.Err(err).Msg("subscribe accounting")

		return err
	}

	for {
		select {
		case msg := <-cli.ReadCh:
			logger.Debug().
				Bytes("payload", msg).
				Msg("Got message")

			er := &response.Error{}

			err = json.Unmarshal(msg, er)
			if err != nil {
				logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")
			}

			if er.Error != nil {
				logger.Fatal().Bytes("response", msg).Err(err).Msg("received error")
			}

			r := &response.AccountingUpdates{}
			err := json.Unmarshal(msg, r)

			if err != nil {
				logger.Fatal().Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			for _, order := range r.Orders {
				storage.UserOrders[order.ID] = order
			}

			eventCh <- 0
		case <-ctx.Done():
			cli.Close()

			return nil
		}
	}
}
