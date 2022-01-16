package response

type GetOrder struct {
	ID    string           `json:"id"`
	Order *AccountingOrder `json:"order"`
}

type Order struct {
	Price  string `json:"price"`
	Amount string `json:"amount"`
	Total  string `json:"total"`
}
