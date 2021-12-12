package storage

import "math/big"

type Balance struct {
	CurrencyCode string
	CurrencyName string
	Available    *big.Float
	Reserved     *big.Float
	Total        *big.Float
	Account      int
}
