package storage

import (
	"math/big"
	"sort"
	"strconv"
	"sync"

	uuid "github.com/satori/go.uuid"
	"github.com/tevino/abool"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/response"
)

type Storage struct {
	pairMx sync.RWMutex
	pairs  map[string]*response.Pair

	userOrdersMx sync.RWMutex
	UserOrders   map[int64]*response.AccountingOrder
	Balances     map[string]*response.Balance
	Deals        []*response.Deal

	orderBooksMx sync.RWMutex
	OrderBooks   map[string]map[string]*Book // base/quoted
}

type Book struct {
	mx sync.RWMutex

	maxBidPrice *big.Float
	minAskPrice *big.Float
	LastPrice   string
	Spread      *big.Float

	Session map[string]*Session

	CompletedBuyOrders  int64
	CompletedSellOrders int64

	bids map[string]*BookOrder
	asks map[string]*BookOrder
}

type BookOrder struct {
	Price     *big.Float
	USDTPrice *big.Float
	Amount    *big.Float
	Total     *big.Float
	USDTTotal *big.Float
}

type Session struct {
	ID               string
	ActiveBuyOrderID int64
	PrevBuyOrderID   int64

	ActiveSellOrderID int64
	PrevSellOrderID   int64

	BuyOrders  map[int64]int64
	SellOrders map[int64]int64

	IsDone *abool.AtomicBool
}

func NewStorage() *Storage {
	return &Storage{
		pairMx:     sync.RWMutex{},
		pairs:      make(map[string]*response.Pair),
		UserOrders: map[int64]*response.AccountingOrder{},
		Balances:   map[string]*response.Balance{},
		Deals:      []*response.Deal{},
		OrderBooks: make(map[string]map[string]*Book),
	}
}

func (s *Storage) RegisterOrderBook(pair *response.Pair) {
	s.orderBooksMx.Lock()
	defer s.orderBooksMx.Unlock()

	if _, ok := s.OrderBooks[pair.BaseCurrency]; !ok {
		s.OrderBooks[pair.BaseCurrency] = make(map[string]*Book)
	}

	if _, ok := s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]; !ok {
		s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency] = &Book{
			mx:                  sync.RWMutex{},
			maxBidPrice:         &big.Float{},
			minAskPrice:         &big.Float{},
			LastPrice:           "",
			Spread:              &big.Float{},
			Session:             make(map[string]*Session),
			CompletedBuyOrders:  0,
			CompletedSellOrders: 0,
			bids:                make(map[string]*BookOrder),
			asks:                make(map[string]*BookOrder),
		}
	}
}

func (s *Storage) GetUserOrder(id int64) *response.AccountingOrder {
	s.userOrdersMx.RLock()
	defer s.userOrdersMx.RUnlock()

	order, ok := s.UserOrders[id]
	if !ok {
		return nil
	}

	return order
}

func (s *Storage) SetUserOrder(order *response.AccountingOrder) {
	s.userOrdersMx.Lock()
	defer s.userOrdersMx.Unlock()

	s.UserOrders[order.ID] = order
}

func (s *Storage) UpdatePairs(pairs []*response.Pair) {
	s.pairMx.Lock()
	defer s.pairMx.Unlock()

	for _, pair := range pairs {
		s.pairs[pair.BaseCurrency+"/"+pair.QuoteCurrency] = pair
	}
}

func (s *Storage) GetPair(pairName string) *response.Pair {
	s.pairMx.RLock()
	defer s.pairMx.RUnlock()

	pair, ok := s.pairs[pairName]
	if !ok {
		return nil
	}

	return pair
}

func (b *Book) NewSession() *Session {
	b.mx.Lock()
	defer b.mx.Unlock()

	id := uuid.NewV4().String()
	session := &Session{
		ID:                id,
		ActiveBuyOrderID:  0,
		PrevBuyOrderID:    0,
		ActiveSellOrderID: 0,
		PrevSellOrderID:   0,
		BuyOrders:         map[int64]int64{},
		SellOrders:        map[int64]int64{},
		IsDone:            abool.New(),
	}

	b.Session[id] = session

	return session
}

func (s *Storage) AddBuyOrder(pair *response.Pair, sid string, oid int64) {
	s.orderBooksMx.Lock()
	defer s.orderBooksMx.Unlock()

	s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Session[sid].BuyOrders[oid] = oid
}

func (s *Storage) AddSellOrder(pair *response.Pair, sid string, oid int64) {
	s.orderBooksMx.Lock()
	defer s.orderBooksMx.Unlock()

	s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Session[sid].SellOrders[oid] = oid
}

func (s *Storage) GetTotalBuyVolume(pair *response.Pair, sid string) (*big.Float, bool) {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Session[sid].BuyOrders {
		if s.UserOrders[oid].TotalBuyVolume == "" {
			continue
		}

		orderTotalBuyVolume, ok := big.NewFloat(0).SetString(s.UserOrders[oid].TotalBuyVolume)
		if !ok {
			return nil, ok
		}

		total.Add(total, orderTotalBuyVolume)
	}

	return total, true
}

func (s *Storage) GetTotalSellVolume(pair *response.Pair, sid string) (*big.Float, bool) {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Session[sid].BuyOrders {
		if s.UserOrders[oid].TotalSellVolume == "" {
			continue
		}

		orderTotalSellVolume, ok := big.NewFloat(0).SetString(s.UserOrders[oid].TotalSellVolume)
		if !ok {
			return nil, ok
		}

		total.Add(total, orderTotalSellVolume)
	}

	return total, true
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

func (s *Storage) CleanUpOldOrders() {
	for _, baseCurrency := range s.OrderBooks {
		for _, book := range baseCurrency {
			for _, sess := range book.Session {
				if sess.IsDone.IsNotSet() {
					continue
				}

				for _, id := range sess.BuyOrders {
					delete(s.UserOrders, id)
				}

				for _, id := range sess.SellOrders {
					delete(s.UserOrders, id)
				}

				delete(book.Session, sess.ID)
			}
		}
	}
}
