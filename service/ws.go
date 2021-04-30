package service

import (
	"context"
	"os"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/client"
	"github.com/soulgarden/kickex-bot/conf"
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

func (s *WS) Start(ctx context.Context, interrupt chan os.Signal) {
	var err error

	s.cli, err = client.NewWsCli(s.cfg, interrupt, s.logger)
	if err != nil {
		s.logger.Fatal().Err(err).Msg("connection error")
		interrupt <- syscall.SIGSTOP
	}

	defer s.cli.Close()

	err = s.cli.Auth()
	if err != nil {
		s.logger.Err(err).Msg("auth error")
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case msg, ok := <-s.cli.ReadCh:
			if !ok {
				s.logger.Err(err).Msg("read channel closed")
				interrupt <- syscall.SIGSTOP

				return
			}

			s.eventBroker.Publish(msg)

		case <-ctx.Done():
			return
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

func (s *WS) CreateOrder(pair, volume, limitPrice string, tradeIntent int) (int64, error) {
	return s.cli.CreateOrder(pair, volume, limitPrice, tradeIntent)
}

func (s *WS) CancelOrder(orderID int64) (int64, error) {
	return s.cli.CancelOrder(orderID)
}

func (s *WS) GetOrder(orderID int64) (int64, error) {
	return s.cli.GetOrder(orderID)
}
