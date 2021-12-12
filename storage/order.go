package storage

import (
	"math/big"
	"time"
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
	TotalFeeQuoted   string
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
