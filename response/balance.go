package response

type Balances struct {
	ID      string     `json:"id"`
	Balance []*Balance `json:"balance"`
}

type Balance struct {
	CurrencyCode string `json:"currencyCode"`
	CurrencyName string `json:"currencyName"`
	Available    string `json:"available"`
	Reserved     string `json:"reserved"`
	Total        string `json:"total"`
	Account      int    `json:"account"`
}
