package request

type GetOrderBookAndSubscribe struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Pair string `json:"pair"`
}
