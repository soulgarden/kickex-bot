package storage

import (
	"sync/atomic"

	"github.com/tevino/abool"

	goAtomic "go.uber.org/atomic"
)

type Session struct {
	ID string `json:"id"`

	activeBuyOrderRequestID goAtomic.String
	ActiveBuyExtOrderID     goAtomic.String   `json:"active_buy_ext_order_id"`
	ActiveBuyOrderID        int64             `json:"active_buy_order_id"`
	PrevBuyOrderID          int64             `json:"prev_buy_order_id"`
	IsNeedToCreateBuyOrder  *abool.AtomicBool `json:"is_need_to_create_buy_order"`

	activeSellOrderRequestID goAtomic.String
	ActiveSellExtOrderID     goAtomic.String   `json:"active_sell_order_id"`
	ActiveSellOrderID        int64             `json:"active_sell_ext_order_id"`
	PrevSellOrderID          int64             `json:"prev_sell_order_id"`
	IsNeedToCreateSellOrder  *abool.AtomicBool `json:"is_need_to_create_sell_order"`

	BuyOrders  map[int64]int64 `json:"buy_orders"`
	SellOrders map[int64]int64 `json:"sell_orders"`

	IsDone *abool.AtomicBool `json:"is_done"`
}

func (s *Session) GetPrevBuyOrderID() int64 {
	return atomic.LoadInt64(&s.PrevBuyOrderID)
}

func (s *Session) SetPrevBuyOrderID(oid int64) {
	atomic.StoreInt64(&s.PrevBuyOrderID, oid)
}

func (s *Session) GetPrevSellOrderID() int64 {
	return atomic.LoadInt64(&s.PrevSellOrderID)
}

func (s *Session) SetPrevSellOrderID(oid int64) {
	atomic.StoreInt64(&s.PrevSellOrderID, oid)
}

func (s *Session) GetActiveBuyOrderID() int64 {
	return atomic.LoadInt64(&s.ActiveBuyOrderID)
}

func (s *Session) SetActiveBuyOrderID(oid int64) {
	atomic.StoreInt64(&s.ActiveBuyOrderID, oid)
}

func (s *Session) GetActiveSellOrderID() int64 {
	return atomic.LoadInt64(&s.ActiveSellOrderID)
}

func (s *Session) SetActiveSellOrderID(oid int64) {
	atomic.StoreInt64(&s.ActiveSellOrderID, oid)
}

func (s *Session) GetActiveBuyExtOrderID() string {
	return s.ActiveBuyExtOrderID.Load()
}

func (s *Session) SetActiveBuyExtOrderID(extID string) {
	s.ActiveBuyExtOrderID.Store(extID)
}

func (s *Session) GetActiveBuyOrderRequestID() string {
	return s.activeBuyOrderRequestID.Load()
}

func (s *Session) SetActiveBuyOrderRequestID(rid string) {
	s.activeBuyOrderRequestID.Store(rid)
}

func (s *Session) GetActiveSellExtOrderID() string {
	return s.ActiveSellExtOrderID.Load()
}

func (s *Session) SetActiveSellExtOrderID(extID string) {
	s.ActiveSellExtOrderID.Store(extID)
}

func (s *Session) GetActiveSellOrderRequestID() string {
	return s.activeSellOrderRequestID.Load()
}

func (s *Session) SetActiveSellOrderRequestID(rid string) {
	s.activeSellOrderRequestID.Store(rid)
}
