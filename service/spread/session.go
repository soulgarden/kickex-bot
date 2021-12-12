package spread

import (
	"math/big"

	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/storage/spread"
)

type Session struct {
	storage *storage.Storage
}

func New(storage *storage.Storage) *Session {
	return &Session{storage: storage}
}

func (s *Session) GetSessTotalBoughtVolume(session *spread.Session) *big.Float {
	total := big.NewFloat(0)

	orders := s.storage.GetUserOrders()

	for _, oid := range session.GetBuyOrders() {
		if orders[oid].TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, orders[oid].TotalBuyVolume)
	}

	return total
}

func (s *Session) GetSessTotalBoughtCost(session *spread.Session) *big.Float {
	total := big.NewFloat(0)

	for _, oid := range session.GetBuyOrders() {
		if s.storage.GetUserOrder(oid).TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, big.NewFloat(0).Mul(
			s.storage.GetUserOrder(oid).TotalBuyVolume,
			s.storage.GetUserOrder(oid).LimitPrice,
		))
	}

	return total
}

func (s *Session) GetSessTotalSoldVolume(session *spread.Session) *big.Float {
	total := big.NewFloat(0)

	for _, oid := range session.GetSellOrders() {
		totalSellVolume := s.storage.GetUserOrder(oid).TotalSellVolume

		if totalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, totalSellVolume)
	}

	return total
}

func (s *Session) GetSessTotalSoldCost(session *spread.Session) *big.Float {
	total := big.NewFloat(0)

	for _, oid := range session.GetSellOrders() {
		if s.storage.GetUserOrder(oid).TotalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, big.NewFloat(0).Mul(
			s.storage.GetUserOrder(oid).TotalSellVolume,
			s.storage.GetUserOrder(oid).LimitPrice,
		))
	}

	return total
}

func (s *Session) GetSessProfit(sess *spread.Session) *big.Float {
	return big.NewFloat(0).Sub(s.GetSessTotalSoldCost(sess), s.GetSessTotalBoughtCost(sess))
}
