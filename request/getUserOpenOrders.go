package request

type GetUsersOpenOrders struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Pair string `json:"pair"`
}
