package service

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"golang.org/x/sync/errgroup"
)

type WS struct {
	cfg         *conf.Bot
	logger      *zerolog.Logger
	eventBroker *broker.Broker
	cli         *client.Client
}

func NewWS(cfg *conf.Bot, eventBroker *broker.Broker, logger *zerolog.Logger) *WS {
	return &WS{cfg: cfg, eventBroker: eventBroker, logger: logger}
}

func (s *WS) Connect(g *errgroup.Group) error {
	var err error

	s.cli, err = client.NewWsCli(s.cfg, g, s.logger)
	if err != nil {
		s.logger.Err(err).Msg("connection error")

		return err
	}

	err = s.cli.Auth()
	if err != nil {
		s.logger.Err(err).Msg("auth error")

		return err
	}

	return nil
}

func (s *WS) Start(ctx context.Context) error {
	s.logger.Warn().Msg("start listen ws")
	defer s.logger.Warn().Msg("stop listen ws")

	for {
		select {
		case msg, ok := <-s.cli.ReadCh:
			if !ok {
				s.logger.Err(dictionary.ErrWsReadChannelClosed).Msg("read channel closed")

				return dictionary.ErrWsReadChannelClosed
			}

			s.eventBroker.Publish(msg)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *WS) SubscribeAccounting(includeDeals bool) (int64, error) {
	return s.cli.SubscribeAccounting(includeDeals)
}

func (s *WS) GetOrderBookAndSubscribe(pairs string) (int64, error) {
	return s.cli.GetOrderBookAndSubscribe(pairs)
}

func (s *WS) GetPairsAndSubscribe() (int64, error) {
	return s.cli.GetPairsAndSubscribe()
}

func (s *WS) CreateOrder(pair, volume, limitPrice string, tradeIntent int) (int64, string, error) {
	return s.cli.CreateOrder(pair, volume, limitPrice, tradeIntent)
}

func (s *WS) CancelOrder(orderID int64) (int64, error) {
	return s.cli.CancelOrder(orderID)
}

func (s *WS) GetOrder(orderID int64) (int64, error) {
	return s.cli.GetOrder(orderID)
}

func (s *WS) GetOrderByExtID(extID string) (int64, error) {
	return s.cli.GetOrderByExtID(extID)
}

func (s *WS) GetBalance() (int64, error) {
	return s.cli.GetBalance()
}

func (s *WS) AlterOrder(pair, volume, limitPrice string, tradeIntent int, orderID int64) (int64, string, error) {
	return s.cli.AlterOrder(pair, volume, limitPrice, tradeIntent, orderID)
}

func (s *WS) Close() {
	s.cli.Close()
}
