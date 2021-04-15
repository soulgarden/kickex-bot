package conf

import (
	"os"

	"github.com/jinzhu/configor"
	"github.com/rs/zerolog/log"
)

type Bot struct {
	APIKey     string `json:"api_key"      required:"true"`
	APIKeyPass string `json:"api_key_pass"      required:"true"`

	DefaultAddr string `json:"default_addr" default:"demo.gate.kickex.com"`
	Scheme      string `json:"scheme" default:"wss"`

	Pair               string `json:"pair" default:"KICK/USDT"`
	MaxCompletedOrders int64  `json:"max_completed_orders"`

	Pairs map[string]*Pair `json:"pairs" required:"true"`

	OrderSleepMS int `json:"order_sleep_ms" default:"50"`

	Debug bool `json:"debug"`
}

type Pair struct {
	PriceStep            string  `json:"price_step" required:"true"`
	SpreadForStartTrade  string  `json:"spread_for_start_trade" required:"true"`
	SpreadForStopTrade   string  `json:"spread_for_stop_trade" required:"true"`
	MaxCompletedOrders   int     `json:"max_completed_orders" required:"true"`
	PricePrecision       int     `json:"price_precision" required:"true"`
	OrderVolumePrecision int     `json:"order_volume_precision" required:"true"`
	TotalBuyAmountInUSDT float64 `json:"total_buy_amount_in_usdt" required:"true"`
}

func New() *Bot {
	c := &Bot{}
	path := os.Getenv("CFG_PATH")

	if path == "" {
		path = "./conf/conf.json"
	}

	if err := configor.New(&configor.Config{ErrorOnUnmatchedKeys: true}).Load(c, path); err != nil {
		log.Fatal().Err(err).Msg("conf validation errors")
	}

	if _, ok := c.Pairs[c.Pair]; !ok {
		log.Fatal().Msg("pair is missing in pairs list")
	}

	return c
}
