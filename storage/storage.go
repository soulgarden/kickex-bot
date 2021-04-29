package storage

import (
	"encoding/json"
	"io/ioutil"
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
	UserOrders   map[int64]*Order
	Balances     map[string]*response.Balance
	Deals        []*response.Deal

	orderBooksMx sync.RWMutex
	OrderBooks   map[string]map[string]*Book `json:"order_books"` // base/quoted
}

type Book struct {
	mx sync.RWMutex

	maxBidPrice *big.Float
	minAskPrice *big.Float
	LastPrice   string
	Spread      *big.Float

	Sessions map[string]*Session `json:"session"`

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
	ID               string `json:"id"`
	ActiveBuyOrderID int64  `json:"active_buy_order_id"`
	PrevBuyOrderID   int64  `json:"prev_buy_order_id"`

	ActiveSellOrderID int64 `json:"active_sell_order_id"`
	PrevSellOrderID   int64 `json:"prev_sell_order_id"`

	BuyOrders  map[int64]int64 `json:"buy_orders"`
	SellOrders map[int64]int64 `json:"sell_orders"`

	IsDone *abool.AtomicBool `json:"is_done"`
}

type Order struct {
	ID               int64
	TradeTimestamp   string
	CreatedTimestamp string
	State            int
	Modifier         int
	Pair             string
	TradeIntent      int
	OrderedVolume    *big.Float
	LimitPrice       *big.Float
	TotalSellVolume  *big.Float
	TotalBuyVolume   *big.Float
	TotalFeeQuoted   string
	TotalFeeExt      string
	Activated        string
	TpActivateLevel  string
	TrailDistance    string
	TpSubmitLevel    string
	TpLimitPrice     string
	SlSubmitLevel    string
	SlLimitPrice     string
	StopTimestamp    string
	TriggeredSide    string
}

func NewStorage() *Storage {
	return &Storage{
		pairMx:     sync.RWMutex{},
		pairs:      make(map[string]*response.Pair),
		UserOrders: map[int64]*Order{},
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
			Sessions:            make(map[string]*Session),
			CompletedBuyOrders:  0,
			CompletedSellOrders: 0,
			bids:                make(map[string]*BookOrder),
			asks:                make(map[string]*BookOrder),
		}
	} else { // loaded from dump
		book := s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]
		book.mx = sync.RWMutex{}
		book.maxBidPrice = &big.Float{}
		book.minAskPrice = &big.Float{}
		book.Spread = &big.Float{}
		book.bids = make(map[string]*BookOrder)
		book.asks = make(map[string]*BookOrder)
	}
}

func (s *Storage) GetUserOrder(id int64) *Order {
	s.userOrdersMx.RLock()
	defer s.userOrdersMx.RUnlock()

	order, ok := s.UserOrders[id]
	if !ok {
		return nil
	}

	return order
}

func (s *Storage) SetUserOrder(order *Order) {
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

	b.Sessions[id] = session

	return session
}

func (s *Storage) AddBuyOrder(pair *response.Pair, sid string, oid int64) {
	s.orderBooksMx.Lock()
	defer s.orderBooksMx.Unlock()

	s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders[oid] = oid
}

func (s *Storage) AddSellOrder(pair *response.Pair, sid string, oid int64) {
	s.orderBooksMx.Lock()
	defer s.orderBooksMx.Unlock()

	s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders[oid] = oid
}

func (s *Storage) GetTotalBuyVolume(pair *response.Pair, sid string) (*big.Float, bool) {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders {
		if s.UserOrders[oid].TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, s.UserOrders[oid].TotalBuyVolume)
	}

	return total, true
}

func (s *Storage) GetTotalSellVolume(pair *response.Pair, sid string) (*big.Float, bool) {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders {
		if s.UserOrders[oid].TotalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, s.UserOrders[oid].TotalSellVolume)
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
			for _, sess := range book.Sessions {
				if sess.IsDone.IsNotSet() {
					continue
				}

				for _, id := range sess.BuyOrders {
					delete(s.UserOrders, id)
				}

				for _, id := range sess.SellOrders {
					delete(s.UserOrders, id)
				}

				delete(book.Sessions, sess.ID)
			}
		}
	}
}

func (s *Storage) DumpSessions(path string) error {
	marshalled, err := json.Marshal(s)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(path, marshalled, 0644)

	return err
}

func (s *Storage) LoadSessions(path string) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	err = json.Unmarshal(data, s)
	if err != nil {
		return err
	}

	return nil
}
