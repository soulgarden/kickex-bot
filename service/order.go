package service

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"syscall"

	"github.com/soulgarden/kickex-bot/dictionary"

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
				o := &storage.Order{
					ID:               r.Order.ID,
					TradeTimestamp:   r.Order.TradeTimestamp,
					CreatedTimestamp: r.Order.CreatedTimestamp,
					State:            r.Order.State,
					Modifier:         r.Order.Modifier,
					Pair:             r.Order.Pair,
					TradeIntent:      r.Order.TradeIntent,
					TotalFeeQuoted:   r.Order.TotalFeeQuoted,
					TotalFeeExt:      r.Order.TotalFeeExt,
					Activated:        r.Order.Activated,
					TpActivateLevel:  r.Order.TpActivateLevel,
					TrailDistance:    r.Order.TrailDistance,
					TpSubmitLevel:    r.Order.TpSubmitLevel,
					TpLimitPrice:     r.Order.LimitPrice,
					SlSubmitLevel:    r.Order.SlSubmitLevel,
					SlLimitPrice:     r.Order.SlLimitPrice,
					StopTimestamp:    r.Order.StopTimestamp,
					TriggeredSide:    r.Order.TriggeredSide,
				}

				o.OrderedVolume, ok = big.NewFloat(0).SetString(r.Order.OrderedVolume)
				if !ok {
					s.logger.Err(dictionary.ErrParseFloat).
						Str("val", r.Order.OrderedVolume).
						Msg("parse string as float")
					interrupt <- syscall.SIGSTOP

					return err
				}

				o.LimitPrice, ok = big.NewFloat(0).SetString(r.Order.LimitPrice)
				if !ok {
					s.logger.Err(dictionary.ErrParseFloat).
						Str("val", r.Order.LimitPrice).
						Msg("parse string as float")
					interrupt <- syscall.SIGSTOP

					return err
				}

				o.TotalSellVolume = big.NewFloat(0)
				if r.Order.TotalSellVolume != "" {
					o.TotalSellVolume, ok = big.NewFloat(0).SetString(r.Order.TotalSellVolume)
					if !ok {
						s.logger.Err(dictionary.ErrParseFloat).
							Str("val", r.Order.TotalSellVolume).
							Msg("parse string as float")
						interrupt <- syscall.SIGSTOP

						return err
					}
				}

				o.TotalBuyVolume = big.NewFloat(0)
				if r.Order.TotalBuyVolume != "" {
					o.TotalBuyVolume, ok = big.NewFloat(0).SetString(r.Order.TotalBuyVolume)
					if !ok {
						s.logger.Err(dictionary.ErrParseFloat).
							Str("val", r.Order.TotalBuyVolume).
							Msg("parse string as float")
						interrupt <- syscall.SIGSTOP

						return err
					}
				}

				s.storage.SetUserOrder(o)
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
