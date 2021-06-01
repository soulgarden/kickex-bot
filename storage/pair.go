package storage

import "math/big"

type Pair struct {
	BaseCurrency    string
	QuoteCurrency   string
	Price           string
	Price24hChange  string
	Volume24hChange string
	Amount24hChange string
	LowPrice24h     string
	HighPrice24h    string
	PriceScale      int
	QuantityScale   int
	VolumeScale     int
	MinQuantity     string
	MinVolume       *big.Float
	State           int
}

func (p *Pair) GetPairName() string {
	return p.BaseCurrency + "/" + p.QuoteCurrency
}