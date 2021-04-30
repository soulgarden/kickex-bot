package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"syscall"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Order struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *broker.Broker
	wsSvc       *WS
	logger      *zerolog.Logger
}

func NewOrder(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	wsSvc *WS,
	logger *zerolog.Logger,
) *Order {
	return &Order{cfg: cfg, storage: storage, eventBroker: eventBroker, wsSvc: wsSvc, logger: logger}
}

func (s *Order) UpdateOrderStates(ctx context.Context, interrupt chan os.Signal) error {
	s.logger.Warn().Msg("order states updater starting...")
	defer s.logger.Warn().Msg("order states updater  stopped")

	eventsCh := s.eventBroker.Subscribe()
	defer s.eventBroker.Unsubscribe(eventsCh)

	oNumber := len(s.storage.UserOrders)
	oNumberProcessed := 0

	reqIds := map[string]bool{}

	for _, o := range s.storage.UserOrders {
		id, err := s.wsSvc.GetOrder(o.ID)
		if err != nil {
			interrupt <- syscall.SIGSTOP

			s.logger.Err(err).Msg("get order")

			return err
		}

		reqIds[strconv.FormatInt(id, 10)] = true
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				interrupt <- syscall.SIGSTOP

				return nil
			}

			msg, ok := e.([]byte)
			if !ok {
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

			if _, ok := reqIds[rid.ID]; !ok {
				continue
			}

			err = s.checkErrorResponse(msg)
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

				s.storage.UpsertUserOrder(o)
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
			s.logger.Err(dictionary.ErrOrderNotFoundOrOutdated).Bytes("response", msg).Msg("received order not found")

			return dictionary.ErrOrderNotFoundOrOutdated
		}

		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return err
	}

	return nil
}
