package arbitrage

import (
	"fmt"
	"math/big"
	"time"

	"github.com/soulgarden/kickex-bot/conf"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/service"
	"github.com/soulgarden/kickex-bot/storage"
)

const sendInterval = time.Second

type Tg struct {
	cfg    *conf.Bot
	tgSvc  *service.Telegram
	sentAt *time.Time
}

func NewTg(cfg *conf.Bot, tgSvc *service.Telegram) *Tg {
	return &Tg{cfg: cfg, tgSvc: tgSvc}
}

func (s *Tg) SendTGBuyBaseArbitrageAvailable(
	baseQuotedPair *storage.Pair,
	startBuyVolume *big.Float,
	quotedSellOrderUSDTAmount *big.Float,
	spread *big.Float,
) {
	now := time.Now()
	if s.sentAt == nil || time.Now().After(s.sentAt.Add(sendInterval)) {
		s.sentAt = &now

		s.sendTGArbitrageAvailable(baseQuotedPair, startBuyVolume, quotedSellOrderUSDTAmount, spread)
	}
}

func (s *Tg) SendTGCheckBuyBaseFinishedWithError(baseQuotedPair *storage.Pair, err error) {
	s.tgSvc.SendAsync(
		fmt.Sprintf(
			`env: %s,
pair %s,
check buy base option finished with error,
error %s`,
			s.cfg.Env,
			baseQuotedPair.GetPairName(),
			err.Error(),
		),
	)
}

func (s *Tg) SendTGOrderCreationEventNotReceived(orderID int64) {
	s.tgSvc.SendAsync(fmt.Sprintf(
		`env: %s,
order creation event not received,
id: %d`,
		s.cfg.Env,
		orderID,
	))
}

func (s *Tg) SendTGSuccess(env, pairName, beforeUSDTAmount, afterUSDTAmount string) {
	s.tgSvc.SendAsync(
		fmt.Sprintf(
			`env: %s,
			arbitrage done,
			pair %s,
			before USDT amount %s,
			after USDT amount %s`,
			env,
			pairName,
			beforeUSDTAmount,
			afterUSDTAmount,
		))
}

func (s *Tg) SendTGFailed(env string, step int, oid int64, pairName string) {
	s.tgSvc.SendAsync(
		fmt.Sprintf(
			`env: %s,
			arbitrage failed on %d step,
			cancel order %d,
			pair %s`,
			env,
			step,
			oid,
			pairName,
		))
}

func (s *Tg) sendTGArbitrageAvailable(
	baseQuotedPair *storage.Pair,
	startBuyVolume *big.Float,
	quotedSellOrderUSDTAmount *big.Float,
	spread *big.Float,
) {
	s.tgSvc.SendAsync(
		fmt.Sprintf(
			`env: %s,
pair %s,
arbitrage available,
before USDT amount %s,
after USDT amount %s,
spread %s`,
			s.cfg.Env,
			baseQuotedPair.GetPairName(),
			startBuyVolume.Text('f', dictionary.ExtendedPrecision),
			quotedSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision),
			spread.Text('f', dictionary.DefaultPrecision),
		),
	)
}

func (s *Tg) SendTGBuyQuotedArbitrageAvailable(
	baseQuotedPair *storage.Pair,
	quotedUSDTPair *storage.Pair,
	baseSellOrderUSDTAmount *big.Float,
	spread *big.Float,
) {
	now := time.Now()
	if s.sentAt == nil || time.Now().After(s.sentAt.Add(sendInterval)) {
		s.sentAt = &now

		s.tgSvc.SendAsync(
			fmt.Sprintf(
				`env: %s,
arbitrage available, but not supported
pair %s,
before USDT amount %s,
after USDT amount %s,
spread %s`,
				s.cfg.Env,
				baseQuotedPair.GetPairName(),
				quotedUSDTPair.MinVolume.Text('f', dictionary.ExtendedPrecision),
				baseSellOrderUSDTAmount.Text('f', dictionary.ExtendedPrecision),
				spread.Text('f', dictionary.DefaultPrecision),
			))
	}
}
