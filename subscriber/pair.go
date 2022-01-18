package subscriber

import (
	"context"
	"fmt"
	"math/big"
	"strconv"

	"github.com/mailru/easyjson"
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

func (s *Pairs) Start(ctx context.Context) error {
	s.logger.Warn().Msg("pairs subscriber starting...")
	defer s.logger.Warn().Msg("pairs subscriber stopped")

	eventsCh := s.eventBroker.Subscribe("pair subscriber")
	defer s.eventBroker.Unsubscribe(eventsCh)

	id, err := s.wsSvc.GetPairsAndSubscribe()
	if err != nil {
		s.logger.Err(err).Msg("get pairs and subscribe")

		return err
	}

	for {
		select {
		case e, ok := <-eventsCh:
			if !ok {
				s.logger.Err(dictionary.ErrEventChannelClosed).Msg("event channel closed")

				return dictionary.ErrEventChannelClosed
			}

			msg, ok := e.([]byte)
			if !ok {
				s.logger.Err(dictionary.ErrCantConvertInterfaceToBytes).Msg(dictionary.ErrCantConvertInterfaceToBytes.Error())

				return dictionary.ErrCantConvertInterfaceToBytes
			}

			rid := &response.ID{}

			err := easyjson.Unmarshal(msg, rid)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
			}

			if strconv.FormatInt(id, dictionary.DefaultIntBase) != rid.ID {
				continue
			}

			err = s.checkErrorResponse(msg)
			if err != nil {
				s.logger.Err(err).Msg("check error response")

				return err
			}

			r := &response.Pairs{}

			err = easyjson.Unmarshal(msg, r)
			if err != nil {
				s.logger.Err(err).Bytes("msg", msg).Msg("unmarshall")

				return err
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

					return dictionary.ErrParseFloat
				}

				p.MinVolume = minVolume

				pairs = append(pairs, p)
			}

			s.storage.UpdatePairs(pairs)

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Pairs) checkErrorResponse(msg []byte) error {
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
