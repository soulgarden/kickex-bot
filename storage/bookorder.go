package storage

import "math/big"

type BookOrder struct {
	Price     *big.Float
	USDTPrice *big.Float
	Amount    *big.Float
	Total     *big.Float
	USDTTotal *big.Float
}
