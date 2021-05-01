package response

type BookResponse struct {
	ID        string   `json:"id"`
	Bids      []*Order `json:"bids"`
	Asks      []*Order `json:"asks"`
	LastPrice struct {
		Price string `json:"price"`
		Type  int    `json:"type"`
	} `json:"lastPrice"`
}

type Order struct {
	Price  string
	Amount string
	Total  string
}

type UserOrdersResponse struct {
	ID         string             `json:"id"`
	OpenOrders []*AccountingOrder `json:"openOrders"`
}

type AccountingUpdates struct {
	ID                string `json:"id"`
	ClientEnvironment struct {
		FavoritePairs    []string `json:"favoritePairs"`
		FavoriteCurrency string   `json:"favoriteCurrency"`
		TariffType       int      `json:"tariffType"`
		AllowSpecialFee  bool     `json:"allowSpecialFee"`
	} `json:"clientEnvironment"`

	Balance []*Balance         `json:"balance"`
	Orders  []*AccountingOrder `json:"orders"`
	Deals   []*Deal            `json:"deals"`
}

type AccountingOrder struct {
	ID               int64  `json:"id"`
	TradeTimestamp   string `json:"tradeTimestamp"`
	CreatedTimestamp string `json:"createdTimestamp"`
	State            int    `json:"state"`
	Modifier         int    `json:"modifier"`
	Pair             string `json:"pair"`
	TradeIntent      int    `json:"tradeIntent"`
	OrderedVolume    string `json:"orderedVolume"`
	LimitPrice       string `json:"limitPrice"`
	TotalSellVolume  string `json:"totalSellVolume"`
	TotalBuyVolume   string `json:"totalBuyVolume"`
	TotalFeeQuoted   string `json:"totalFeeQuoted"`
	TotalFeeExt      string `json:"totalFeeExt"`
	Activated        string `json:"activated"`
	TpActivateLevel  string `json:"tpActivateLevel"`
	TrailDistance    string `json:"trailDistance"`
	TpSubmitLevel    string `json:"tp_submit_level"`
	TpLimitPrice     string `json:"tpLimitPrice"`
	SlSubmitLevel    string `json:"slSubmitLevel"`
	SlLimitPrice     string `json:"slLimitPrice"`
	StopTimestamp    string `json:"stopTimestamp"`
	TriggeredSide    string `json:"triggeredSide"`
}

type Deal struct {
	Timestamp  string `json:"timestamp"`
	OrderID    int64  `json:"orderId"`
	Pair       string `json:"pair"`
	Price      string `json:"price"`
	SellVolume string `json:"sellVolume"`
	BuyVolume  string `json:"buyVolume"`
	FeeQuoted  string `json:"feeQuoted"`
	FeeExt     string `json:"feeExt"`
}

type CreatedOrder struct {
	ID      string `json:"id"`
	OrderID int64  `json:"order_id"`
}
