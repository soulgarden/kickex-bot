package conf

import (
	"os"

	"github.com/jinzhu/configor"
	"github.com/rs/zerolog/log"
)

type Bot struct {
	APIKey     string `json:"api_key"       required:"true"`
	APIKeyPass string `json:"api_key_pass"  required:"true"`

	DefaultAddr string `json:"default_addr" default:"demo.gate.kickex.com"`
	Scheme      string `json:"scheme"       default:"wss"`

	MaxCompletedOrders int64 `json:"max_completed_orders"`

	Pairs         []string `json:"pairs"          required:"true"`
	TrackingPairs []string `json:"tracking_pairs" required:"true"`

	SpreadForStartBuy      string `json:"spread_for_start_buy"       required:"true"`
	SpreadForStartSell     string `json:"spread_for_start_sell"      required:"true"`
	SpreadForStopBuyTrade  string `json:"spread_for_stop_buy_trade"  required:"true"`
	SpreadForStopSellTrade string `json:"spread_for_stop_sell_trade" required:"true"`

	TotalBuyInUSDT string `json:"total_buy_in_usdt" required:"true"`

	Telegram struct {
		Token  string `json:"token"`
		ChatID int64  `json:"chat_id"`
	} `json:"telegram"`

	Env             string `json:"env"`
	StorageDumpPath string `json:"storage_dump_path" default:"./storage/%s.spread.state.json"`

	Debug bool `json:"debug"`
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

	return c
}
