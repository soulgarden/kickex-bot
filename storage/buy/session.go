package buy

import (
	"math/big"
	"sync"
	"sync/atomic"

	uuid "github.com/satori/go.uuid"

	goAtomic "go.uber.org/atomic"
)

type Session struct {
	mx sync.RWMutex

	ID string `json:"id"`

	activeBuyOrderRequestID string
	ActiveBuyExtOrderID     string `json:"active_buy_ext_order_id"`

	PrevBuyOrderID   int64 `json:"prev_buy_order_id"`
	ActiveBuyOrderID int64 `json:"active_buy_order_id"`

	CompletedBuyOrders goAtomic.Int64 `json:"completed_buy_orders"`

	BuyTotal *big.Float `json:"buy_total"`

	BuyOrders map[int64]int64 `json:"buy_orders"`

	IsNeedToCreateBuyOrder bool `json:"is_need_to_create_buy_order"`
	IsDone                 bool `json:"is_done"`
}

func NewSession(buyVolume *big.Float) *Session {
	return &Session{
		mx:                      sync.RWMutex{},
		ID:                      uuid.NewV4().String(),
		activeBuyOrderRequestID: "",
		ActiveBuyExtOrderID:     "",
		ActiveBuyOrderID:        0,
		PrevBuyOrderID:          0,
		IsNeedToCreateBuyOrder:  true,
		CompletedBuyOrders:      goAtomic.Int64{},
		BuyTotal:                buyVolume,
		BuyOrders:               map[int64]int64{},
		IsDone:                  false,
	}
}

func (s *Session) GetBuyOrders() map[int64]int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.BuyOrders
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

	return atomic.LoadInt64(&s.PrevBuyOrderID)
}

func (s *Session) SetPrevBuyOrderID(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	atomic.StoreInt64(&s.PrevBuyOrderID, oid)
}

func (s *Session) GetActiveBuyOrderID() int64 {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return atomic.LoadInt64(&s.ActiveBuyOrderID)
}

func (s *Session) SetActiveBuyOrderID(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	atomic.StoreInt64(&s.ActiveBuyOrderID, oid)
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

func (s *Session) GetIsDone() bool {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.IsDone
}

func (s *Session) SetIsDone(isDone bool) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.IsDone = isDone
}

func (s *Session) SetBuyOrderExecutedFlags(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.CompletedBuyOrders.Add(1)

	atomic.StoreInt64(&s.PrevBuyOrderID, 0)
	atomic.StoreInt64(&s.ActiveBuyOrderID, oid)

	s.ActiveBuyExtOrderID = ""
	s.activeBuyOrderRequestID = ""

	s.IsDone = true
}

func (s *Session) AddBuyOrder(oid int64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.BuyOrders[oid] = oid
}
