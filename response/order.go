package response

type GetOrder struct {
	ID    string           `json:"id"`
	Order *AccountingOrder `json:"order"`
}
