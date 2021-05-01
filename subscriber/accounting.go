package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/service"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/conf"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Accounting struct {
	cfg           *conf.Bot
	storage       *storage.Storage
	wsEventBroker *broker.Broker
	wsSvc         *service.WS
	balanceSvc    *service.Balance
	logger        *zerolog.Logger
}

func NewAccounting(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	wsSvc *service.WS,
	balanceSvc *service.Balance,
	logger *zerolog.Logger,
) *Accounting {
	return &Accounting{
		cfg:           cfg,
		storage:       storage,
		wsEventBroker: eventBroker,
		wsSvc:         wsSvc,
		balanceSvc:    balanceSvc,
		logger:        logger,
	}
}

func (s *Accounting) Start(ctx context.Context, interrupt chan os.Signal, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	s.logger.Warn().Msg("accounting subscriber starting...")
	defer s.logger.Warn().Msg("accounting subscriber stopped")

	eventsCh := s.wsEventBroker.Subscribe()
	defer s.wsEventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.SubscribeAccounting(false)
	if err != nil {
		s.logger.Err(err).Msg("subscribe accounting")
		interrupt <- syscall.SIGSTOP

		return
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGSTOP

				return
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGSTOP

				return
			}

			rid := &response.ID{}

			err := json.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return
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

				return
			}

			r := &response.AccountingUpdates{}
			err = json.Unmarshal(msg, r)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGSTOP

				return
			}

			for _, order := range r.Orders {
				o := &storage.Order{
					ID:               order.ID,
					TradeTimestamp:   order.TradeTimestamp,
					CreatedTimestamp: order.CreatedTimestamp,
					State:            order.State,
					Modifier:         order.Modifier,
					Pair:             order.Pair,
					TradeIntent:      order.TradeIntent,
					TotalFeeQuoted:   order.TotalFeeQuoted,
					TotalFeeExt:      order.TotalFeeExt,
					Activated:        order.Activated,
					TpActivateLevel:  order.TpActivateLevel,
					TrailDistance:    order.TrailDistance,
					TpSubmitLevel:    order.TpSubmitLevel,
					TpLimitPrice:     order.LimitPrice,
					SlSubmitLevel:    order.SlSubmitLevel,
					SlLimitPrice:     order.SlLimitPrice,
					StopTimestamp:    order.StopTimestamp,
					TriggeredSide:    order.TriggeredSide,
				}

				o.OrderedVolume, ok = big.NewFloat(0).SetString(order.OrderedVolume)
				if !ok {
					s.logger.Err(dictionary.ErrParseFloat).
						Str("val", order.OrderedVolume).
						Msg("parse string as float")

					interrupt <- syscall.SIGSTOP

					return
				}

				o.LimitPrice, ok = big.NewFloat(0).SetString(order.LimitPrice)
				if !ok {
					s.logger.Err(dictionary.ErrParseFloat).
						Str("val", order.LimitPrice).
						Msg("parse string as float")
					interrupt <- syscall.SIGSTOP

					return
				}

				o.TotalSellVolume = big.NewFloat(0)
				if order.TotalSellVolume != "" {
					o.TotalSellVolume, ok = big.NewFloat(0).SetString(order.TotalSellVolume)
					if !ok {
						s.logger.Err(dictionary.ErrParseFloat).
							Str("val", order.TotalSellVolume).
							Msg("parse string as float")

						interrupt <- syscall.SIGSTOP

						return
					}
				}

				o.TotalBuyVolume = big.NewFloat(0)
				if order.TotalBuyVolume != "" {
					o.TotalBuyVolume, ok = big.NewFloat(0).SetString(order.TotalBuyVolume)
					if !ok {
						s.logger.Err(dictionary.ErrParseFloat).
							Str("val", order.TotalBuyVolume).
							Msg("parse string as float")

						interrupt <- syscall.SIGSTOP

						return
					}
				}

				s.storage.UpsertUserOrder(o)

				pair := strings.Split(o.Pair, "/")

				s.storage.OrderBooks[pair[0]][pair[1]].EventBroker.Publish(0) //tdo: check existence
			}

			err = s.balanceSvc.UpdateStorageBalances(r.Balance)
			if err != nil {
				s.logger.Err(err).
					Msg("update storage balances")

				interrupt <- syscall.SIGSTOP

				return
			}

			s.storage.Deals = append(s.storage.Deals, r.Deals...)
		case <-ctx.Done():
			interrupt <- syscall.SIGSTOP

			return
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
		err = fmt.Errorf("%w: %s", dictionary.ErrResponse, er.Error.Reason)
		s.logger.Err(err).Bytes("response", msg).Msg("received error")

		return err
	}

	return nil
}
