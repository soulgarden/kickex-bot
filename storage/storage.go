package storage

import (
	"encoding/json"
	"io/ioutil"
	"math/big"
	"sync"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/response"
)

type Storage struct {
	pairMx sync.RWMutex
	pairs  map[string]*Pair

	ordersMx   sync.RWMutex
	UserOrders map[int64]*Order `json:"user_orders"`

	balanceMx sync.RWMutex
	balances  map[string]*Balance

	Deals []*response.Deal `json:"deals"`

	OrderBooks map[string]map[string]*Book `json:"order_books"` // base/quoted
}

func NewStorage() *Storage {
	return &Storage{
		pairMx:     sync.RWMutex{},
		pairs:      make(map[string]*Pair),
		ordersMx:   sync.RWMutex{},
		UserOrders: map[int64]*Order{},
		balanceMx:  sync.RWMutex{},
		balances:   map[string]*Balance{},
		Deals:      []*response.Deal{},
		OrderBooks: make(map[string]map[string]*Book),
	}
}

func (s *Storage) RegisterOrderBook(pair *Pair, eventBroker *broker.Broker) *Book {
	s.ordersMx.Lock()
	defer s.ordersMx.Unlock()

	if _, ok := s.OrderBooks[pair.BaseCurrency]; !ok {
		s.OrderBooks[pair.BaseCurrency] = make(map[string]*Book)
	}

	if _, ok := s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]; !ok {
		s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency] = NewBook(pair, eventBroker)
	} else { // loaded from dump
		book := s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency]
		ResetDumpedBook(book, pair, eventBroker)
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
	s.ordersMx.RLock()
	defer s.ordersMx.RUnlock()

	order, ok := s.UserOrders[id]
	if !ok {
		return nil
	}

	return order
}

func (s *Storage) GetUserOrders() map[int64]*Order {
	s.ordersMx.RLock()
	defer s.ordersMx.RUnlock()

	return s.UserOrders
}

func (s *Storage) UpsertUserOrder(order *Order) {
	s.ordersMx.Lock()
	defer s.ordersMx.Unlock()

	s.UserOrders[order.ID] = order
}

func (s *Storage) UpdatePairs(pairs []*Pair) {
	s.pairMx.Lock()
	defer s.pairMx.Unlock()

	for _, pair := range pairs {
		s.pairs[pair.BaseCurrency+"/"+pair.QuoteCurrency] = pair
	}
}

func (s *Storage) GetPair(pairName string) *Pair {
	s.pairMx.RLock()
	defer s.pairMx.RUnlock()

	pair, ok := s.pairs[pairName]
	if !ok {
		return nil
	}

	return pair
}

func (s *Storage) AddBuyOrder(pair *Pair, sid string, oid int64) {
	s.ordersMx.Lock()
	defer s.ordersMx.Unlock()

	s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders[oid] = oid
}

func (s *Storage) AddSellOrder(pair *Pair, sid string, oid int64) {
	s.ordersMx.Lock()
	defer s.ordersMx.Unlock()

	s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders[oid] = oid
}

func (s *Storage) GetSessTotalBoughtVolume(pair *Pair, sid string) *big.Float {
	s.ordersMx.RLock()
	defer s.ordersMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders {
		if s.UserOrders[oid].TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, s.UserOrders[oid].TotalBuyVolume)
	}

	return total
}

func (s *Storage) GetSessTotalBoughtCost(pair *Pair, sid string) *big.Float {
	s.ordersMx.RLock()
	defer s.ordersMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].BuyOrders {
		if s.UserOrders[oid].TotalBuyVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, big.NewFloat(0).Mul(s.UserOrders[oid].TotalBuyVolume, s.UserOrders[oid].LimitPrice))
	}

	return total
}

func (s *Storage) GetSessTotalSoldVolume(pair *Pair, sid string) *big.Float {
	s.ordersMx.RLock()
	defer s.ordersMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders {
		if s.UserOrders[oid].TotalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, s.UserOrders[oid].TotalSellVolume)
	}

	return total
}

func (s *Storage) GetSessTotalSoldCost(pair *Pair, sid string) *big.Float {
	s.ordersMx.RLock()
	defer s.ordersMx.RUnlock()

	total := big.NewFloat(0)

	for _, oid := range s.OrderBooks[pair.BaseCurrency][pair.QuoteCurrency].Sessions[sid].SellOrders {
		if s.UserOrders[oid].TotalSellVolume.Cmp(dictionary.ZeroBigFloat) == 0 {
			continue
		}

		total.Add(total, big.NewFloat(0).Mul(s.UserOrders[oid].TotalSellVolume, s.UserOrders[oid].LimitPrice))
	}

	return total
}

func (s *Storage) GetSessProfit(pair *Pair, sid string) *big.Float {
	return big.NewFloat(0).Sub(s.GetSessTotalSoldCost(pair, sid), s.GetSessTotalBoughtCost(pair, sid))
}

func (s *Storage) CleanUpOldOrders() {
	s.ordersMx.Lock()
	defer s.ordersMx.Unlock()

	for _, baseCurrency := range s.OrderBooks {
		for _, book := range baseCurrency {
			for _, sess := range book.Sessions {
				if !sess.IsDone {
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
