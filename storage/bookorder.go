package storage

import "math/big"

type BookOrder struct {
	Price  *big.Float
	Amount *big.Float
	Total  *big.Float
}
