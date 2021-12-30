package storage

import (
	"encoding/json"
	"io/ioutil"

	"github.com/soulgarden/kickex-bot/dictionary"

	"github.com/soulgarden/kickex-bot/response"
)

type DumpStorage struct {
	st *Storage

	UserOrders map[int64]*Order            `json:"user_orders"`
	Deals      []*response.Deal            `json:"deals"`
	OrderBooks map[string]map[string]*Book `json:"order_books"`
}

func NewDumpStorage(st *Storage) *DumpStorage {
	d := &DumpStorage{
		UserOrders: st.userOrders,
		Deals:      st.deals,
		OrderBooks: st.orderBooks,
	}

	d.st = st

	return d
}

func (d *DumpStorage) DumpStorage(path string) error {
	marshalled, err := json.Marshal(d)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(path, marshalled, dictionary.DumpFilePermissions)

	return err
}

func (d *DumpStorage) Recover(path string) error {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	err = json.Unmarshal(data, d)
	if err != nil {
		return err
	}

	d.st.userOrders = d.UserOrders
	d.st.deals = d.Deals
	d.st.orderBooks = d.OrderBooks

	return nil
}
