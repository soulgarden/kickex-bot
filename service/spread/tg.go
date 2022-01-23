package spread

import (
	"fmt"
	"math/big"

	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
	storageSpread "github.com/soulgarden/kickex-bot/storage/spread"
)

type Tg struct {
	cfg       *conf.Bot
	pair      *storage.Pair
	tgSvc     *service.Telegram
	sessSvc   *Session
	orderBook *storage.Book
}

func NewTg(cfg *conf.Bot, pair *storage.Pair, tgSvc *service.Telegram, sessSvc *Session, orderBook *storage.Book) *Tg {
	return &Tg{cfg: cfg, pair: pair, tgSvc: tgSvc, sessSvc: sessSvc, orderBook: orderBook}
}

func (s *Tg) sendTGAsyncOrderReachedDoneState(sess *storageSpread.Session, order *storage.Order) {
	s.tgSvc.SendAsync(fmt.Sprintf(
		`env: %s,
buy order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s,
cost: %s,
total profit: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		s.sessSvc.GetSessTotalBoughtVolume(sess).Text('f', s.pair.QuantityScale),
		s.sessSvc.GetSessTotalBoughtCost(sess).Text('f', s.pair.PriceScale),
		s.orderBook.GetProfit().Text('f', s.pair.PriceScale),
	))
}

func (s *Tg) SellOrderReachedDoneState(
	sess *storageSpread.Session,
	order *storage.Order,
	soldVolume *big.Float,
	boughtVolume *big.Float,
) {
	s.tgSvc.SendAsync(fmt.Sprintf(
		`env: %s,
sell order reached done state,
pair: %s,
id: %d,
price: %s,
volume: %s,
cost: %s,
profit: %s,
total profit: %s,
not sold volume: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		order.ID,
		order.LimitPrice.Text('f', s.pair.PriceScale),
		soldVolume.Text('f', s.pair.QuantityScale),
		s.sessSvc.GetSessTotalSoldCost(sess).Text('f', s.pair.PriceScale),
		s.sessSvc.GetSessProfit(sess).Text('f', s.pair.PriceScale),
		s.orderBook.GetProfit().Text('f', s.pair.PriceScale),
		big.NewFloat(0).Sub(boughtVolume, soldVolume).Text('f', s.pair.QuantityScale),
	))
}

func (s *Tg) sendTGAsyncAllowedToCreateNewSession(o *storage.Order) {
	s.tgSvc.SendAsync(fmt.Sprintf(
		`env: %s,
pair: %s,
order price: %s,
min ask price: %s,
allowed to create new session after 1h of inability to create an order`,
		s.cfg.Env,
		s.pair.GetPairName(),
		o.LimitPrice.Text('f', s.pair.PriceScale),
		s.orderBook.GetMinAskPrice().Text('f', s.pair.PriceScale),
	))
}

func (s *Tg) OldSessionStarted(sess *storageSpread.Session) {
	s.tgSvc.SendAsync(fmt.Sprintf(
		`env: %s,
old sesession started,
id: %s,
pair: %s,
total bought: %s,
total sell: %s,
total profit: %s`,
		s.cfg.Env,
		s.pair.GetPairName(),
		sess.ID,
		s.sessSvc.GetSessTotalBoughtCost(sess).Text('f', s.pair.PriceScale),
		s.sessSvc.GetSessTotalSoldCost(sess).Text('f', s.pair.PriceScale),
		s.orderBook.GetProfit().Text('f', s.pair.PriceScale),
	))
}
