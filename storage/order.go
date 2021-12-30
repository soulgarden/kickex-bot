package storage

import (
	"math/big"
	"strconv"
	"time"

	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/response"
)

type Order struct {
	ID               int64
	TradeTimestamp   string
	CreatedTimestamp time.Time
	State            int
	Modifier         int
	Pair             string
	TradeIntent      int
	OrderedVolume    *big.Float
	LimitPrice       *big.Float
	TotalSellVolume  *big.Float
	TotalBuyVolume   *big.Float
	TotalFeeQuoted   *big.Float
	TotalFeeExt      string
	Activated        string
	TpActivateLevel  string
	TrailDistance    string
	TpSubmitLevel    string
	TpLimitPrice     string
	SlSubmitLevel    string
	SlLimitPrice     string
	StopTimestamp    string
	TriggeredSide    string
}

func NewOrderByResponse(r *response.AccountingOrder) (*Order, error) {
	createdTS, err := strconv.ParseInt(r.CreatedTimestamp, dictionary.DefaultIntBase, 0)
	if err != nil {
		return nil, err
	}

	o := &Order{
		ID:               r.ID,
		TradeTimestamp:   r.TradeTimestamp,
		CreatedTimestamp: time.Unix(0, createdTS),
		State:            r.State,
		Modifier:         r.Modifier,
		Pair:             r.Pair,
		TradeIntent:      r.TradeIntent,
		OrderedVolume:    &big.Float{},
		LimitPrice:       &big.Float{},
		TotalSellVolume:  &big.Float{},
		TotalBuyVolume:   &big.Float{},
		TotalFeeQuoted:   &big.Float{},
		TotalFeeExt:      r.TotalFeeExt,
		Activated:        r.Activated,
		TpActivateLevel:  r.TpActivateLevel,
		TrailDistance:    r.TrailDistance,
		TpSubmitLevel:    r.TpSubmitLevel,
		TpLimitPrice:     r.LimitPrice,
		SlSubmitLevel:    r.SlSubmitLevel,
		SlLimitPrice:     r.SlLimitPrice,
		StopTimestamp:    r.StopTimestamp,
		TriggeredSide:    r.TriggeredSide,
	}

	err = o.setStrAsBigFloat(r.OrderedVolume, o.OrderedVolume)
	if err != nil {
		return nil, err
	}

	err = o.setStrAsBigFloat(r.LimitPrice, o.LimitPrice)
	if err != nil {
		return nil, err
	}

	err = o.setStrAsBigFloat(r.TotalSellVolume, o.TotalSellVolume)
	if err != nil {
		return nil, err
	}

	err = o.setStrAsBigFloat(r.TotalBuyVolume, o.TotalBuyVolume)
	if err != nil {
		return nil, err
	}

	err = o.setStrAsBigFloat(r.TotalFeeQuoted, o.TotalFeeQuoted)

	return o, err
}

func (s *Order) setStrAsBigFloat(str string, val *big.Float) error {
	var ok bool

	if str != "" {
		_, ok = val.SetString(str)
		if !ok {
			return dictionary.ErrParseFloat
		}
	}

	return nil
}
