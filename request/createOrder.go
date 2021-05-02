package request

type CreateOrder struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Fields     *CreateOrderFields `json:"fields"`
	ExternalID string             `json:"externalId,omitempty"`
}

type CreateOrderFields struct {
	Pair          string `json:"pair"`
	OrderedVolume string `json:"ordered_volume"`
	LimitPrice    string `json:"limit_price,omitempty"`
	TradeIntent   int    `json:"trade_intent"`
	Modifier      int    `json:"modifier,omitempty"`
}
