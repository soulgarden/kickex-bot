package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Balance struct {
	storage     *storage.Storage
	eventBroker *broker.Broker
	wsSvc       *WS
	logger      *zerolog.Logger
}

func NewBalance(storage *storage.Storage, eventBroker *broker.Broker, wsSvc *WS, logger *zerolog.Logger) *Balance {
	return &Balance{storage: storage, eventBroker: eventBroker, wsSvc: wsSvc, logger: logger}
}

func (s *Balance) GetBalance(ctx context.Context, interrupt chan os.Signal) error {
	eventsCh := s.eventBroker.Subscribe()
	defer s.eventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.GetBalance()
	if err != nil {
		s.logger.Warn().Msg("get balance")

		interrupt <- syscall.SIGSTOP

		return err
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGSTOP

				return nil
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGSTOP

				return dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}

			err := json.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return err
			}

			if strconv.FormatInt(id, 10) != rid.ID {
				continue
			}

			s.logger.Debug().
				Bytes("payload", msg).
				Msg("got message")

			err = s.checkErrorResponse(msg)
			if err != nil {
				s.logger.Err(err).Msg("check error response")

				interrupt <- syscall.SIGSTOP

				return err
			}

			r := &response.Balances{}
			err = json.Unmarshal(msg, r)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return err
			}

			err = s.UpdateStorageBalances(r.Balance)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return err
			}

			return nil

		case <-ctx.Done():
			return nil
		case <-time.After(time.Minute):
			s.logger.Error().Msg("update order state timeout")
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

func (s *Balance) checkErrorResponse(msg []byte) error {
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
