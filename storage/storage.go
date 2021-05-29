package storage

import (
	"encoding/json"
	"io/ioutil"
	"math/big"
	"sync"

	goAtomic "go.uber.org/atomic"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/response"
)

type Storage struct {
	pairMx sync.RWMutex
	pairs  map[string]*response.Pair

	userOrdersMx sync.RWMutex
	UserOrders   map[int64]*Order `json:"user_orders"`

	balanceMx sync.RWMutex
	balances  map[string]*Balance

	Deals []*response.Deal `json:"deals"`

	orderBooksMx sync.RWMutex
	OrderBooks   map[string]map[string]*Book `json:"order_books"` // base/quoted
}

func NewStorage() *Storage {
	return &Storage{
		pairMx:       sync.RWMutex{},
		pairs:        make(map[string]*response.Pair),
		userOrdersMx: sync.RWMutex{},
		UserOrders:   map[int64]*Order{},
		balanceMx:    sync.RWMutex{},
		balances:     map[string]*Balance{},
		Deals:        []*response.Deal{},
		orderBooksMx: sync.RWMutex{},
		OrderBooks:   make(map[string]map[string]*Book),
	}
}

func (s *Storage) RegisterOrderBook(pair *response.Pair, eventBroker *broker.Broker) *Book {
	s.orderBooksMx.Lock()
	defer s.orderBooksMx.Unlock()

	if _, ok := s.OrderBooks[pair.BaseCurrency]; !ok {
		s.OrderBooks[pair.BaseCurrency] = make(map[string]*Book)
	}

	if _, ok := s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]; !ok {
		s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency] = &Book{
			mx:              sync.RWMutex{},
			maxBidPrice:     &big.Float{},
			minAskPrice:     &big.Float{},
			LastPrice:       "",
			Spread:          &big.Float{},
			Sessions:        make(map[string]*Session),
			ActiveSessionID: goAtomic.String{},
			Profit:          big.NewFloat(0),
			bids:            make(map[string]*BookOrder),
			asks:            make(map[string]*BookOrder),
			EventBroker:     eventBroker,
		}
	} else { // loaded from dump
		book := s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]
		book.mx = sync.RWMutex{}
		book.maxBidPrice = &big.Float{}
		book.minAskPrice = &big.Float{}
		book.Spread = &big.Float{}
		book.bids = make(map[string]*BookOrder)
		book.asks = make(map[string]*BookOrder)
		book.EventBroker = eventBroker
	}

	return s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]
}

func (s *Storage) GetBalance(currency string) *Balance {
	s.balanceMx.RLock()
	defer s.balanceMx.RUnlock()

	balance, ok := s.balances[currency]
	if !ok {
		return nil
	}

	return balance
}

func (s *Storage) UpsertBalance(currency string, balance *Balance) {
	s.balanceMx.Lock()
	defer s.balanceMx.Unlock()

	s.balances[currency] = balance
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

func (s *Storage) GetUserOrders() map[int64]*Order {
	s.userOrdersMx.RLock()
	defer s.userOrdersMx.RUnlock()

	return s.UserOrders
}

func (s *Storage) UpsertUserOrder(order *Order) {
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

func (s *Storage) GetSessTotalBuyVolume(pair *response.Pair, sid string) *big.Float {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders {
		if s.UserOrders[oid].TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, s.UserOrders[oid].TotalBuyVolume)
	}

	return total
}

func (s *Storage) GetSessTotalBuyCost(pair *response.Pair, sid string) *big.Float {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders {
		if s.UserOrders[oid].TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, big.NewFloat(0).Mul(s.UserOrders[oid].TotalBuyVolume, s.UserOrders[oid].LimitPrice))
	}

	return total
}

func (s *Storage) GetSessTotalSellVolume(pair *response.Pair, sid string) *big.Float {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders {
		if s.UserOrders[oid].TotalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, s.UserOrders[oid].TotalSellVolume)
	}

	return total
}

func (s *Storage) GetSessTotalSellCost(pair *response.Pair, sid string) *big.Float {
	s.orderBooksMx.RLock()
	defer s.orderBooksMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders {
		if s.UserOrders[oid].TotalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, big.NewFloat(0).Mul(s.UserOrders[oid].TotalSellVolume, s.UserOrders[oid].LimitPrice))
	}

	return total
}

func (s *Storage) GetSessProfit(pair *response.Pair, sid string) *big.Float {
	return big.NewFloat(0).Sub(s.GetSessTotalSellCost(pair, sid), s.GetSessTotalBuyCost(pair, sid))
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

	err = ioutil.WriteFile(path, marshalled, 0600)

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
