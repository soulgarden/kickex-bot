package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/service"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
	"github.com/soulgarden/kickex-bot/storage"
)

type Pairs struct {
	cfg         *conf.Bot
	storage     *storage.Storage
	eventBroker *broker.Broker
	wsSvc       *service.WS
	logger      *zerolog.Logger
}

func NewPairs(
	cfg *conf.Bot,
	storage *storage.Storage,
	eventBroker *broker.Broker,
	wsSvc *service.WS,
	logger *zerolog.Logger,
) *Pairs {
	return &Pairs{cfg: cfg, storage: storage, eventBroker: eventBroker, wsSvc: wsSvc, logger: logger}
}

func (s *Pairs) Start(ctx context.Context, interrupt chan<- os.Signal, wg *sync.WaitGroup) {
	defer func() {
		wg.Done()
	}()

	s.logger.Warn().Msg("pairs subscriber starting...")
	defer s.logger.Warn().Msg("pairs subscriber stopped")

	eventsCh := s.eventBroker.Subscribe("pair subscriber")
	defer s.eventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.GetPairsAndSubscribe()
	if err != nil {
		s.logger.Err(err).Msg("get pairs and subscribe")

		interrupt <- syscall.SIGINT

		return
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Warn().Msg("event channel closed")

				interrupt <- syscall.SIGINT

				return
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				interrupt <- syscall.SIGINT

				return
			}

			rid := &response.ID{}
			err := json.Unmarshal(msg, rid)

			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return
			}

			if strconv.FormatInt(id, dictionary.DefaultIntBase) != rid.ID {
				continue
			}

			err = s.checkErrorResponse(msg)
			if err != nil {
				s.logger.Err(err).Msg("check error response")

				interrupt <- syscall.SIGINT

				return
			}

			r := &response.Pairs{}

			err = json.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				interrupt <- syscall.SIGINT

				return
			}

			pairs := []*storage.Pair{}

			for _, pair := range r.Pairs {
				p := &storage.Pair{
					BaseCurrency:    pair.BaseCurrency,
					QuoteCurrency:   pair.QuoteCurrency,
					Price:           pair.Price,
					Price24hChange:  pair.Price24hChange,
					Volume24hChange: pair.Volume24hChange,
					Amount24hChange: pair.Amount24hChange,
					LowPrice24h:     pair.LowPrice24h,
					HighPrice24h:    pair.HighPrice24h,
					PriceScale:      pair.PriceScale,
					QuantityScale:   pair.QuantityScale,
					VolumeScale:     pair.VolumeScale,
					MinQuantity:     pair.MinQuantity,
					State:           pair.State,
				}

				minVolume, ok := big.NewFloat(0).SetString(pair.MinVolume)
				if !ok {
					s.logger.Err(dictionary.ErrParseFloat).
						Str("val", pair.MinVolume).
						Msg("parse string as float")

					interrupt <- syscall.SIGINT

					return
				}

				p.MinVolume = minVolume

				pairs = append(pairs, p)
			}

			s.storage.UpdatePairs(pairs)

		case <-ctx.Done():
			interrupt <- syscall.SIGINT

			return
		}
	}
}

func (s *Pairs) checkErrorResponse(msg []byte) error {
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
