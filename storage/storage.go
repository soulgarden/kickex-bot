package storage

import (
	"math/big"
	"sort"
	"sync"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/response"
)

type Storage struct {
	UserOrders map[int64]*response.AccountingOrder
	Balances   []*response.Balance
	Deals      []*response.Deal
	Book       *Book
}

func NewStorage() *Storage {
	return &Storage{
		Book: &Book{
			bids:   make(map[string]*response.Order),
			asks:   make(map[string]*response.Order),
			Spread: dictionary.ZeroBigFloat,
		},
		UserOrders: map[int64]*response.AccountingOrder{},
		Balances:   []*response.Balance{},
		Deals:      []*response.Deal{},
	}
}

type Book struct {
	mx sync.RWMutex

	bids        map[string]*response.Order
	asks        map[string]*response.Order
	maxBidPrice *big.Float
	minAskPrice *big.Float
	LastPrice   string
	Spread      *big.Float

	ActiveBuyOrderID  int64
	ActiveSellOrderID int64

	CompletedBuyOrders  int64
	CompletedSellOrders int64
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

func (b *Book) AddBid(price string, bid *response.Order) bool {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.bids[price] = bid

	return b.updateMaxBidPrice()
}

func (b *Book) GetBid(price string) *response.Order {
	b.mx.RLock()
	defer b.mx.RUnlock()

	val, ok := b.bids[price]
	if !ok {
		return nil
	}

	return val
}

func (b *Book) DeleteBid(price string) bool {
	b.mx.Lock()
	defer b.mx.Unlock()

	delete(b.bids, price)

	return b.updateMaxBidPrice()
}

func (b *Book) AddAsk(price string, ask *response.Order) bool {
	b.mx.Lock()
	defer b.mx.Unlock()

	b.asks[price] = ask

	return b.updateMinAskPrice()
}

func (b *Book) GetAsk(price string) *response.Order {
	b.mx.RLock()
	defer b.mx.RUnlock()

	val, ok := b.asks[price]
	if !ok {
		return nil
	}

	return val
}

func (b *Book) DeleteAsk(price string) bool {
	b.mx.Lock()
	defer b.mx.Unlock()

	delete(b.asks, price)

	return b.updateMinAskPrice()
}

func (b *Book) updateMaxBidPrice() bool {
	if len(b.bids) > 0 {
		bidsPrices := []string{}

		for price := range b.bids {
			bidsPrices = append(bidsPrices, price)
		}

		sort.Strings(bidsPrices)

		maxBidPrice, ok := big.NewFloat(0).SetString(bidsPrices[len(bidsPrices)-1])
		if !ok {
			return ok
		}

		b.setMaxBidPrice(maxBidPrice)
	} else {
		b.setMaxBidPrice(dictionary.ZeroBigFloat)
	}

	return true
}

func (b *Book) updateMinAskPrice() bool {
	if len(b.asks) > 0 {
		askPrices := []string{}

		for price := range b.asks {
			askPrices = append(askPrices, price)
		}

		sort.Strings(askPrices)

		minAskPrice, ok := big.NewFloat(0).SetString(askPrices[0])
		if !ok {
			return ok
		}

		b.setMinAskPrice(minAskPrice)
	} else {
		b.setMinAskPrice(dictionary.ZeroBigFloat)
	}

	return true
}
