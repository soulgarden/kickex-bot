package spread

import (
	"math/big"
	"sync"

	uuid "github.com/satori/go.uuid"
)

type Session struct {
	mx sync.RWMutex

	ID string `json:"id"`

	activeBuyOrderRequestID string
	ActiveBuyExtOrderID     string `json:"active_buy_ext_order_id"`

	activeSellOrderRequestID string
	ActiveSellExtOrderID     string `json:"active_sell_ext_order_id"`

	PrevBuyOrderID    int64 `json:"prev_buy_order_id"`
	ActiveBuyOrderID  int64 `json:"active_buy_order_id"`
	ActiveSellOrderID int64 `json:"active_sell_order_id"`
	PrevSellOrderID   int64 `json:"prev_sell_order_id"`

	CompletedBuyOrders  int64 `json:"completed_buy_orders"`
	CompletedSellOrders int64 `json:"completed_sell_orders"`

	BuyTotal   *big.Float `json:"buy_total"`
	SellVolume *big.Float `json:"sell_total"`

	BuyOrders  map[int64]int64 `json:"buy_orders"`
	SellOrders map[int64]int64 `json:"sell_orders"`

	IsNeedToCreateBuyOrder  bool `json:"is_need_to_create_buy_order"`
	IsNeedToCreateSellOrder bool `json:"is_need_to_create_sell_order"`
	IsDone                  bool `json:"is_done"`
}

func (s *Session) GetBuyOrders() map[int64]int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.BuyOrders
}

func (s *Session) GetSellOrders() map[int64]int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.SellOrders
}

func NewSession(buyVolume *big.Float) *Session {
	return &Session{
		mx:                       sync.RWMutex{},
		ID:                       uuid.NewV4().String(),
		activeBuyOrderRequestID:  "",
		ActiveBuyExtOrderID:      "",
		ActiveBuyOrderID:         0,
		PrevBuyOrderID:           0,
		IsNeedToCreateBuyOrder:   true,
		activeSellOrderRequestID: "",
		ActiveSellExtOrderID:     "",
		ActiveSellOrderID:        0,
		PrevSellOrderID:          0,
		IsNeedToCreateSellOrder:  false,
		CompletedBuyOrders:       0,
		CompletedSellOrders:      0,
		BuyTotal:                 buyVolume,
		SellVolume:               &big.Float{},
		BuyOrders:                map[int64]int64{},
		SellOrders:               map[int64]int64{},
		IsDone:                   false,
	}
}

func (s *Session) GetIsNeedToCreateSellOrder() bool {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.IsNeedToCreateSellOrder
}

func (s *Session) SetIsNeedToCreateSellOrder(isNeedToCreateSellOrder bool) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.IsNeedToCreateSellOrder = isNeedToCreateSellOrder
}

func (s *Session) GetIsNeedToCreateBuyOrder() bool {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.IsNeedToCreateBuyOrder
}

func (s *Session) SetIsNeedToCreateBuyOrder(isNeedToCreateBuyOrder bool) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.IsNeedToCreateBuyOrder = isNeedToCreateBuyOrder
}

func (s *Session) GetPrevBuyOrderID() int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.PrevBuyOrderID
}

func (s *Session) SetPrevBuyOrderID(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.PrevBuyOrderID = oid
}

func (s *Session) GetPrevSellOrderID() int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.PrevSellOrderID
}

func (s *Session) SetPrevSellOrderID(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.PrevSellOrderID = oid
}

func (s *Session) GetActiveBuyOrderID() int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.ActiveBuyOrderID
}

func (s *Session) SetActiveBuyOrderID(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.ActiveBuyOrderID = oid
}

func (s *Session) GetActiveSellOrderID() int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.ActiveSellOrderID
}

func (s *Session) SetActiveSellOrderID(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.ActiveSellOrderID = oid
}

func (s *Session) GetActiveBuyExtOrderID() string {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.ActiveBuyExtOrderID
}

func (s *Session) SetActiveBuyExtOrderID(extID string) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.ActiveBuyExtOrderID = extID
}

func (s *Session) GetActiveBuyOrderRequestID() string {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.activeBuyOrderRequestID
}

func (s *Session) SetActiveBuyOrderRequestID(rid string) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.activeBuyOrderRequestID = rid
}

func (s *Session) GetActiveSellExtOrderID() string {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.ActiveSellExtOrderID
}

func (s *Session) SetActiveSellExtOrderID(extID string) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.ActiveSellExtOrderID = extID
}

func (s *Session) GetActiveSellOrderRequestID() string {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.activeSellOrderRequestID
}

func (s *Session) SetActiveSellOrderRequestID(rid string) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.activeSellOrderRequestID = rid
}

func (s *Session) GetBuyTotal() *big.Float {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.BuyTotal
}

func (s *Session) SetBuyTotal(buyTotal *big.Float) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.BuyTotal = buyTotal
}

func (s *Session) GetSellVolume() *big.Float {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.SellVolume
}

func (s *Session) SetSellVolume(sellVolume *big.Float) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.SellVolume = sellVolume
}

func (s *Session) GetIsDone() bool {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.IsDone
}

func (s *Session) SetBuyOrderDoneFlags(oid int64, sellVolume *big.Float) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.CompletedBuyOrders++

	s.PrevBuyOrderID = 0
	s.ActiveBuyOrderID = oid

	s.ActiveBuyExtOrderID = ""
	s.activeBuyOrderRequestID = ""

	s.ActiveSellOrderID = 0

	s.ActiveSellExtOrderID = ""
	s.activeSellOrderRequestID = ""
	s.IsNeedToCreateSellOrder = true
	s.SellVolume = sellVolume
}

func (s *Session) SetSellOrderDoneFlags() {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.CompletedSellOrders++

	s.PrevSellOrderID = 0
	s.ActiveSellOrderID = 0

	s.ActiveSellExtOrderID = ""
	s.activeSellOrderRequestID = ""

	s.IsDone = true
}

func (s *Session) SetBuyOrderCancelledFlags() {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.ActiveBuyExtOrderID = ""
	s.activeBuyOrderRequestID = ""
	s.IsNeedToCreateBuyOrder = true
}

func (s *Session) SetSellOrderCancelledFlags() {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.ActiveSellExtOrderID = ""
	s.activeSellOrderRequestID = ""
	s.IsNeedToCreateSellOrder = true
}

func (s *Session) AddBuyOrder(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.BuyOrders[oid] = oid
}

func (s *Session) AddSellOrder(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.SellOrders[oid] = oid
}
