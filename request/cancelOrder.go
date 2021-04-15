package request

type CancelOrder struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	OrderID int64  `json:"order_id"`
}
