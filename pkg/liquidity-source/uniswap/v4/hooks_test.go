package uniswapv4

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestHasSwapPermissions(t *testing.T) {
	hook := "0xd73339564ac99f3e09b0ebc80603ff8b796500c0"
	t.Log(HasSwapPermissions(common.HexToAddress(hook)))
}

func TestUnknownHookUnsafe(t *testing.T) {
	const (
		// addresses below only set the permission bit(s) named, nothing else.
		beforeSwapOnly         = "0x0000000000000000000000000000000000000080" // bit 7
		afterSwapOnly          = "0x0000000000000000000000000000000000000040" // bit 6
		beforeSwapReturnsDelta = "0x0000000000000000000000000000000000000008" // bit 3
		afterSwapReturnsDelta  = "0x0000000000000000000000000000000000000004" // bit 2
		noSwapPermissionsAtAll = "0x0000000000000000000000000000000000000000"
	)

	tests := []struct {
		name         string
		address      string
		isDynamicFee bool
		wantUnsafe   bool
	}{
		{"no permissions at all is always safe", noSwapPermissionsAtAll, true, false},
		{"revert/accounting-only hook on static-fee pool is safe", beforeSwapOnly, false, false},
		{"revert/accounting-only hook can still revert, but that's not a pricing risk", afterSwapOnly, false, false},
		{"beforeSwap hook on a dynamic-fee pool can override the fee: unsafe", beforeSwapOnly, true, true},
		{"afterSwap can't override fee even on a dynamic-fee pool: safe", afterSwapOnly, true, false},
		{"beforeSwapReturnsDelta is unsafe regardless of fee mode", beforeSwapReturnsDelta, false, true},
		{"afterSwapReturnsDelta is unsafe regardless of fee mode", afterSwapReturnsDelta, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := common.HexToAddress(tt.address)
			if got := UnknownHookUnsafe(addr, tt.isDynamicFee); got != tt.wantUnsafe {
				t.Errorf("UnknownHookUnsafe(%s, dynamicFee=%v) = %v, want %v",
					tt.address, tt.isDynamicFee, got, tt.wantUnsafe)
			}
		})
	}
}
