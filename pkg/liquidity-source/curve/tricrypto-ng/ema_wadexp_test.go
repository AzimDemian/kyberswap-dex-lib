package tricryptong

import (
	"testing"
	"github.com/KyberNetwork/int256"
)

func TestWadExpDirect(t *testing.T) {
	inputs := []int64{
		-20000000000000000,   // dt=12, ma=600
		-40000000000000000,   // dt=24, ma=600
		-100000000000000000,  // dt=60, ma=600
	}
	for _, x := range inputs {
		xi := new(int256.Int).SetInt64(x)
		result, err := _snekmate_wad_exp(xi)
		if err != nil {
			t.Fatalf("wad_exp(%d) error: %v", x, err)
		}
		t.Logf("Go wad_exp(%d) = %s", x, result.Dec())
	}
}
