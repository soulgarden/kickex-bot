package storage

import (
	"math/big"
	"sort"
	"strconv"
	"sync"

	uuid "github.com/satori/go.uuid"
	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/dictionary"
	"github.com/tevino/abool"
	goAtomic "go.uber.org/atomic"
)

type Book struct {
	mx sync.RWMutex

	maxBidPrice *big.Float
	minAskPrice *big.Float
	LastPrice   string
	Spread      *big.Float

	Sessions        map[string]*Session `json:"session"`
	ActiveSessionID goAtomic.String

	CompletedBuyOrders  int64
	CompletedSellOrders int64

	bids map[string]*BookOrder
	asks map[string]*BookOrder

	EventBroker *broker.Broker
}

func (b *Book) GetSpread() *big.Float {
	b.mx.RLock()
	defer b.mx.RUnlock()

	return b.Spread
}

func (b *Book) SetSpread(spread *big.Float) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.Spread = spread
}

func (b *Book) GetMaxBidPrice() *big.Float {
	b.mx.RLock()
	defer b.mx.RUnlock()

	return b.maxBidPrice
}

func (b *Book) setMaxBidPrice(maxBidPrice *big.Float) {
	b.maxBidPrice = maxBidPrice
}

func (b *Book) GetMinAskPrice() *big.Float {
	b.mx.RLock()
	defer b.mx.RUnlock()

	return b.minAskPrice
}

func (b *Book) setMinAskPrice(minAskPrice *big.Float) {
	b.minAskPrice = minAskPrice
}

func (b *Book) AddBid(price string, bid *BookOrder) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.bids[price] = bid
}

func (b *Book) GetBid(price string) *BookOrder {
	b.mx.RLock()
	defer b.mx.RUnlock()

	val, ok := b.bids[price]
	if !ok {
		return nil
	}

	return val
}

func (b *Book) DeleteBid(price string) {
	b.mx.Lock()
	defer b.mx.Unlock()

	delete(b.bids, price)
}

func (b *Book) AddAsk(price string, ask *BookOrder) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.asks[price] = ask
}

func (b *Book) GetAsk(price string) *BookOrder {
	b.mx.RLock()
	defer b.mx.RUnlock()

	val, ok := b.asks[price]
	if !ok {
		return nil
	}

	return val
}

func (b *Book) DeleteAsk(price string) {
	b.mx.Lock()
	defer b.mx.Unlock()

	delete(b.asks, price)
}

func (b *Book) UpdateMaxBidPrice() bool {
	b.mx.Lock()
	defer b.mx.Unlock()

	if len(b.bids) > 0 {
		bidsPrices := []float64{}
		strToFloatPrices := map[float64]string{}

		for price := range b.bids {
			pf, err := strconv.ParseFloat(price, 64)
			if err != nil {
				return false
			}

			bidsPrices = append(bidsPrices, pf)
			strToFloatPrices[pf] = price
		}

		sort.Float64s(bidsPrices)

		maxBidPrice, ok := big.NewFloat(0).SetString(strToFloatPrices[bidsPrices[len(bidsPrices)-1]])
		if !ok {
			return ok
		}

		b.setMaxBidPrice(maxBidPrice)
	} else {
		b.setMaxBidPrice(dictionary.ZeroBigFloat)
	}

	return true
}

func (b *Book) UpdateMinAskPrice() bool {
	b.mx.Lock()
	defer b.mx.Unlock()

	if len(b.asks) > 0 {
		askPrices := []float64{}
		strToFloatPrices := map[float64]string{}

		for price := range b.asks {
			pf, err := strconv.ParseFloat(price, 64)
			if err != nil {
				return false
			}

			askPrices = append(askPrices, pf)
			strToFloatPrices[pf] = price
		}

		sort.Float64s(askPrices)

		minAskPrice, ok := big.NewFloat(0).SetString(strToFloatPrices[askPrices[0]])
		if !ok {
			return ok
		}

		b.setMinAskPrice(minAskPrice)
	} else {
		b.setMinAskPrice(dictionary.ZeroBigFloat)
	}

	return true
}

func (b *Book) NewSession() *Session {
	b.mx.Lock()
	defer b.mx.Unlock()

	sess := &Session{
		ID:                      "",
		ActiveBuyExtOrderID:     0,
		ActiveBuyOrderID:        0,
		PrevBuyOrderID:          0,
		IsNeedToCreateBuyOrder:  abool.New(),
		ActiveSellExtOrderID:    0,
		ActiveSellOrderID:       0,
		PrevSellOrderID:         0,
		IsNeedToCreateSellOrder: abool.New(),

		BuyOrders:  map[int64]int64{},
		SellOrders: map[int64]int64{},
		IsDone:     abool.New(),
	}

	sess.ID = uuid.NewV4().String()
	b.ActiveSessionID.Store(sess.ID)

	sess.IsNeedToCreateBuyOrder.Set()

	b.Sessions[sess.ID] = sess

	return sess
}
