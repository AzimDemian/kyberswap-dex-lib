package klik

import (
	"bytes"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

var (
	hookABI    = mustParseABI(hookABIJson)
	factoryABI = mustParseABI(factoryABIJson)
)

func mustParseABI(data []byte) abi.ABI {
	parsed, err := abi.JSON(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	return parsed
}
