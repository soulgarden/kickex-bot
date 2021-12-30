package storage

import (
	"math/big"
	"sort"
	"strconv"
	"sync"

	"github.com/soulgarden/kickex-bot/storage/buy"
	"github.com/soulgarden/kickex-bot/storage/spread"

	"github.com/soulgarden/kickex-bot/broker"
	"github.com/soulgarden/kickex-bot/dictionary"
	goAtomic "go.uber.org/atomic"
)

type Book struct {
	mx sync.RWMutex

	storage *Storage

	maxBidPrice *big.Float
	minAskPrice *big.Float
	LastPrice   string
	Spread      *big.Float

	SpreadSessions map[string]*spread.Session `json:"spread_sessions"`
	BuySessions    map[string]*buy.Session    `json:"buy_sessions"`

	SpreadActiveSessionID goAtomic.String
	BuyActiveSessionID    goAtomic.String

	Profit       *big.Float `json:"profit"`
	BoughtVolume *big.Float `json:"bought_volume"`
	BoughtCost   *big.Float `json:"bought_cost"`

	bids map[string]*BookOrder
	asks map[string]*BookOrder

	OrderBookEventBroker *broker.Broker
	pair                 *Pair
}

func NewBook(storage *Storage, pair *Pair, orderBookEventBroker *broker.Broker) *Book {
	return &Book{
		mx:                    sync.RWMutex{},
		storage:               storage,
		maxBidPrice:           &big.Float{},
		minAskPrice:           &big.Float{},
		LastPrice:             "",
		Spread:                &big.Float{},
		SpreadSessions:        make(map[string]*spread.Session),
		BuySessions:           make(map[string]*buy.Session),
		SpreadActiveSessionID: goAtomic.String{},
		BuyActiveSessionID:    goAtomic.String{},
		Profit:                big.NewFloat(0),
		BoughtVolume:          big.NewFloat(0),
		BoughtCost:            big.NewFloat(0),
		bids:                  make(map[string]*BookOrder),
		asks:                  make(map[string]*BookOrder),
		OrderBookEventBroker:  orderBookEventBroker,
		pair:                  pair,
	}
}

func ResetDumpedBook(book *Book, pair *Pair, eventBroker *broker.Broker) *Book {
	book.mx = sync.RWMutex{}
	book.maxBidPrice = &big.Float{}
	book.minAskPrice = &big.Float{}
	book.Spread = &big.Float{}
	book.bids = make(map[string]*BookOrder)
	book.asks = make(map[string]*BookOrder)
	book.OrderBookEventBroker = eventBroker
	book.pair = pair

	return book
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

func (b *Book) GetMaxBid() *BookOrder {
	b.mx.RLock()
	defer b.mx.RUnlock()

	val, ok := b.bids[b.maxBidPrice.Text('f', b.pair.PriceScale)]
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

func (b *Book) GetMinAsk() *BookOrder {
	b.mx.RLock()
	defer b.mx.RUnlock()

	val, ok := b.asks[b.minAskPrice.Text('f', b.pair.PriceScale)]
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
			pf, err := strconv.ParseFloat(price, dictionary.DefaultNumberBitSize)
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
			pf, err := strconv.ParseFloat(price, dictionary.DefaultNumberBitSize)
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

func (b *Book) GetProfit() *big.Float {
	b.mx.RLock()
	defer b.mx.RUnlock()

	return b.Profit
}

func (b *Book) SubProfit(v *big.Float) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.Profit.Sub(b.Profit, v)
}

func (b *Book) AddProfit(v *big.Float) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.Profit.Add(b.Profit, v)
}

func (b *Book) GetBoughtCost() *big.Float {
	b.mx.RLock()
	defer b.mx.RUnlock()

	return b.BoughtCost
}

func (b *Book) AddBoughtCost(v *big.Float) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.BoughtCost.Add(b.BoughtCost, v)
}

func (b *Book) GetBoughtVolume() *big.Float {
	b.mx.RLock()
	defer b.mx.RUnlock()

	return b.BoughtVolume
}

func (b *Book) AddBoughtVolume(v *big.Float) {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.BoughtVolume.Add(b.BoughtVolume, v)
}

func (b *Book) NewSpreadSession(buyVolume *big.Float) *spread.Session {
	b.mx.Lock()
	defer b.mx.Unlock()

	sess := spread.NewSession(buyVolume)

	b.SpreadActiveSessionID.Store(sess.ID)

	b.SpreadSessions[sess.ID] = sess

	return sess
}

func (b *Book) NewBuySession(buyVolume *big.Float) *buy.Session {
	b.mx.Lock()
	defer b.mx.Unlock()

	sess := buy.NewSession(buyVolume)

	b.BuyActiveSessionID.Store(sess.ID)

	b.BuySessions[sess.ID] = sess

	return sess
}
