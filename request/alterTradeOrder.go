package request

type AlterTradeOrder struct {
	CreateOrder
	OrderID    int64  `json:"orderId"`
	ExternalID string `json:"externalId"`
}
