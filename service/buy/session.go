package buy

import (
	"math/big"

	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/soulgarden/kickex-bot/storage"
	"github.com/soulgarden/kickex-bot/storage/buy"
)

type Session struct {
	storage *storage.Storage
}

func New(storage *storage.Storage) *Session {
	return &Session{storage: storage}
}

func (s *Session) GetSessTotalBoughtVolume(session *buy.Session) *big.Float {
	total := big.NewFloat(0)

	orders := s.storage.GetUserOrders()

	for _, oid := range session.GetBuyOrders() {
		if s.storage.GetUserOrder(oid).TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, orders[oid].TotalBuyVolume)
	}

	return total
}

func (s *Session) GetSessTotalBoughtCost(sess *buy.Session) *big.Float {
	total := big.NewFloat(0)

	for _, oid := range sess.GetBuyOrders() {
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
