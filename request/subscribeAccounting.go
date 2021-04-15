package request

type SubscribeAccounting struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	IncludeDeals bool   `json:"includeDeals"`
}
