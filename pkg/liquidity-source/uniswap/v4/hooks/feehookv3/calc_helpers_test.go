package feehookv3

import "math/big"

func bigFromStr(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad number: " + s)
	}
	return n
}

func mulDivDown(x string, num, den int64) string {
	return new(big.Int).Quo(new(big.Int).Mul(bigFromStr(x), big.NewInt(num)), big.NewInt(den)).String()
}

func subStr(a, b string) string {
	return new(big.Int).Sub(bigFromStr(a), bigFromStr(b)).String()
}

func addStr(a, b string) string {
	return new(big.Int).Add(bigFromStr(a), bigFromStr(b)).String()
}
