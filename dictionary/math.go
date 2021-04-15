package dictionary

import "math/big"

var ZeroBigFloat, _ = big.NewFloat(0).SetString("0")      //nolint: gochecknoglobals
var MaxPercentFloat, _ = big.NewFloat(0).SetString("100") //nolint: gochecknoglobals
