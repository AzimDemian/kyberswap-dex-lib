package feebps

import (
	"bytes"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

var hookABI = func() abi.ABI {
	parsed, err := abi.JSON(bytes.NewReader(hookABIJson))
	if err != nil {
		panic(err)
	}
	return parsed
}()
