package storage

import (
	"sync"

	"github.com/soulgarden/kickex-bot/broker"

	"github.com/soulgarden/kickex-bot/response"
)

type Storage struct {
	mx         sync.RWMutex
	pairs      map[string]*Pair
	userOrders map[int64]*Order

	balances map[string]*Balance

	deals []*response.Deal

	orderBooks map[string]map[string]*Book // base/quoted
}

func NewStorage() *Storage {
	return &Storage{
		mx:         sync.RWMutex{},
		pairs:      make(map[string]*Pair),
		userOrders: map[int64]*Order{},
		balances:   map[string]*Balance{},
		deals:      []*response.Deal{},
		orderBooks: make(map[string]map[string]*Book),
	}
}

func (s *Storage) RegisterOrderBook(pair *Pair, eventBroker *broker.Broker) *Book {
	s.mx.Lock()
	defer s.mx.Unlock()

	if _, ok := s.orderBooks[pair.BaseCurrency]; !ok {
		s.orderBooks[pair.BaseCurrency] = make(map[string]*Book)
	}

	if _, ok := s.orderBooks[pair.BaseCurrency][pair.QuoteCurrency]; !ok {
		s.orderBooks[pair.BaseCurrency][pair.QuoteCurrency] = NewBook(s, pair, eventBroker)
	} else { // loaded from dump
		book := s.orderBooks[pair.BaseCurrency][pair.QuoteCurrency]
		ResetDumpedBook(book, pair, eventBroker)
	}

	return s.orderBooks[pair.BaseCurrency][pair.QuoteCurrency]
}

func (s *Storage) GetOrderBook(base, quoted string) *Book {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.getOrderBook(base, quoted)
}

func (s *Storage) getOrderBook(base, quoted string) *Book {
	baseOrderBooks, ok := s.orderBooks[base]
	if !ok {
		return nil
	}

	book, ok := baseOrderBooks[quoted]
	if !ok {
		return nil
	}

	return book
}

func (s *Storage) AppendDeals(deals ...*response.Deal) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.deals = append(s.deals, deals...)
}

func (s *Storage) GetBalance(currency string) *Balance {
	s.mx.RLock()
	defer s.mx.RUnlock()

	balance, ok := s.balances[currency]
	if !ok {
		return nil
	}

	return balance
}

func (s *Storage) UpsertBalance(currency string, balance *Balance) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.balances[currency] = balance
}

func (s *Storage) GetUserOrder(id int64) *Order {
	s.mx.RLock()
	defer s.mx.RUnlock()

	order, ok := s.userOrders[id]
	if !ok {
		return nil
	}

	return order
}

func (s *Storage) GetUserOrders() map[int64]*Order {
	s.mx.RLock()
	defer s.mx.RUnlock()

	return s.userOrders
}

func (s *Storage) UpsertUserOrder(order *Order) {
	s.mx.Lock()
	defer s.mx.Unlock()

	s.userOrders[order.ID] = order
}

func (s *Storage) UpdatePairs(pairs []*Pair) {
	s.mx.Lock()
	defer s.mx.Unlock()

	for _, pair := range pairs {
		s.pairs[pair.BaseCurrency+"/"+pair.QuoteCurrency] = pair
	}
}

func (s *Storage) GetPair(pairName string) *Pair {
	s.mx.RLock()
	defer s.mx.RUnlock()

	pair, ok := s.pairs[pairName]
	if !ok {
		return nil
	}

	return pair
}

func (s *Storage) CleanUpOldOrders() {
	s.mx.Lock()
	defer s.mx.Unlock()

	for _, baseCurrency := range s.orderBooks {
		for _, book := range baseCurrency {
			for _, sess := range book.SpreadSessions {
				if !sess.GetIsDone() {
					continue
				}

				for _, id := range sess.BuyOrders {
					delete(s.userOrders, id)
				}

				for _, id := range sess.SellOrders {
					delete(s.userOrders, id)
				}

				delete(book.SpreadSessions, sess.ID)
			}
		}
	}
}
