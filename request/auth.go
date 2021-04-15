package request

type Auth struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	APIKey   string `json:"apiKey"`
	Password string `json:"password"`
}
