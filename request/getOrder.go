package request

type GetOrder struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	OrderID    int64  `json:"orderId,omitempty"`
	ExternalID string `json:"externalId,omitempty"`
}
