package service

import (
	"math/big"

	"github.com/rs/zerolog"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/storage"
)

type Conversion struct {
	storage *storage.Storage
	logger  *zerolog.Logger
}

func NewConversion(storage *storage.Storage, logger *zerolog.Logger) *Conversion {
	return &Conversion{storage: storage, logger: logger}
}

func (s *Conversion) GetUSDTPrice(currency string) (*big.Float, error) {
	var quotedToUSDTPrice *big.Float

	var ok bool

	if currency == dictionary.USDT {
		return big.NewFloat(1), nil
	}

	p := s.storage.GetPair(currency + "/" + dictionary.USDT)

	quotedToUSDTPrice, ok = big.NewFloat(0).SetString(p.Price)
	if !ok {
		s.logger.
			Err(dictionary.ErrParseFloat).
			Str("val", p.Price).
			Msg("parse string as float")

		return nil, dictionary.ErrParseFloat
	}

	s.logger.Debug().
		Str("pair", currency+"/"+dictionary.USDT).
		Str("price", quotedToUSDTPrice.String()).
		Msg("price quoted to usdt")

	return quotedToUSDTPrice, nil
}

func (s *Conversion) GetAskUSDTPrice(currency string) *big.Float {
	if currency == dictionary.USDT {
		return big.NewFloat(1)
	}

	return s.storage.GetOrderBook(currency, dictionary.USDT).GetMinAskPrice()
}

func (s *Conversion) GetBidUSDTPrice(currency string) *big.Float {
	if currency == dictionary.USDT {
		return big.NewFloat(1)
	}

	return s.storage.GetOrderBook(currency, dictionary.USDT).GetMaxBidPrice()
}
