package response

type Pairs struct {
	ID    string  `json:"id"`
	Pairs []*Pair `json:"pairs"`
}

type Pair struct {
	BaseCurrency    string `json:"baseCurrency"`
	QuoteCurrency   string `json:"quoteCurrency"`
	Price           string `json:"price"`
	Price24hChange  string `json:"price24hChange"`
	Volume24hChange string `json:"volume24hChange"`
	Amount24hChange string `json:"amount_24_h_change"`
	LowPrice24h     string `json:"lowPrice24h"`
	HighPrice24h    string `json:"highPrice24h"`
	PriceScale      int    `json:"priceScale"`
	QuantityScale   int    `json:"quantityScale"`
	VolumeScale     int    `json:"volumeScale"`
	MinQuantity     string `json:"minQuantity"`
	MinVolume       string `json:"minVolume"`
	State           int    `json:"state"`
}
