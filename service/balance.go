package service

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/mailru/easyjson"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

const balanceTimeout = time.Second * 10

type Balance struct {
	storage        *storage.Storage
	eventBroker    *broker.Broker
	accEventBroker *broker.Broker
	wsSvc          *WS
	logger         *zerolog.Logger
}

func NewBalance(
	storage *storage.Storage,
	eventBroker *broker.Broker,
	accEventBroker *broker.Broker,
	wsSvc *WS,
	logger *zerolog.Logger,
) *Balance {
	return &Balance{
		storage:        storage,
		eventBroker:    eventBroker,
		accEventBroker: accEventBroker,
		wsSvc:          wsSvc,
		logger:         logger,
	}
}

func (s *Balance) GetBalance(ctx context.Context, interrupt chan<- os.Signal) error {
	eventsCh := s.eventBroker.Subscribe("get balance")
	defer s.eventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.GetBalance()
	if err != nil {
		s.logger.Warn().Msg("get balance")

		interrupt <- syscall.SIGINT

		return err
	}

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

			if strconv.FormatInt(id, dictionary.DefaultIntBase) != rid.ID {
				continue
			}

			s.logger.Debug().
				Bytes("payload", msg).
				Msg("got message")

			err = s.checkErrorResponse(msg)
			if err != nil {
				s.logger.Err(err).Msg("check error response")

				interrupt <- syscall.SIGINT

				return err
			}

			r := &response.Balances{}
			err = easyjson.Unmarshal(msg, r)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return err
			}

			err = s.UpdateStorageBalances(r.Balance)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return err
			}

			return nil

		case <-ctx.Done():
			return nil
		case <-time.After(time.Minute):
			s.logger.Err(dictionary.ErrUpdateOrderStateTimeout).Msg("update balance timeout")

			return dictionary.ErrUpdateOrderStateTimeout
		}
	}
}

func (s *Balance) UpdateStorageBalances(balances []*response.Balance) error {
	for _, balance := range balances {
		available, ok := big.NewFloat(0).SetString(balance.Available)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Str("val", balance.Available).
				Msg("parse string as float")

			return dictionary.ErrParseFloat
		}

		reserved, ok := big.NewFloat(0).SetString(balance.Available)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Str("val", balance.Reserved).
				Msg("parse string as float")

			return dictionary.ErrParseFloat
		}

		total, ok := big.NewFloat(0).SetString(balance.Total)
		if !ok {
			s.logger.Err(dictionary.ErrParseFloat).
				Str("val", balance.Total).
				Msg("parse string as float")

			return dictionary.ErrParseFloat
		}

		s.storage.UpsertBalance(balance.CurrencyCode, &storage.Balance{
			CurrencyCode: balance.CurrencyCode,
			CurrencyName: balance.CurrencyName,
			Available:    available,
			Reserved:     reserved,
			Total:        total,
			Account:      balance.Account,
		})
	}

	return nil
}

func (s *Balance) WaitForSufficientBalance(ctx context.Context, pair string, amount *big.Float) error {
	if s.storage.GetBalance(pair).Available.Cmp(amount) >= 0 {
		return nil
	}

	accEventCh := s.accEventBroker.Subscribe("wait for sufficient balance")

	defer s.accEventBroker.Unsubscribe(accEventCh)

	for {
		select {
		case _, ok := <-accEventCh:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return dictionary.ErrEventChannelClosed
			}

			if s.storage.GetBalance(pair).Available.Cmp(amount) == 1 {
				return nil
			}
		case <-ctx.Done():
			return nil
		case <-time.After(balanceTimeout):
			return dictionary.ErrWaitBalanceUpdate
		}
	}
}

func (s *Balance) checkErrorResponse(msg []byte) error {
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
