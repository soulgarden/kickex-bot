package response

type GetOrder struct {
	ID    string           `json:"id"`
	Order *AccountingOrder `json:"order"`
}

type Order struct {
	Price  string
	Amount string
	Total  string
}
