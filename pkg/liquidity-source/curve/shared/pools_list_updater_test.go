package shared

import (
	"testing"
	"time"
)

type stubVolumeProvider struct {
	volumes map[string]float64
}

func (s *stubVolumeProvider) GetVolume(address string) (float64, bool) {
	v, ok := s.volumes[address]
	return v, ok
}

func TestFilterContext_EmptyFilterPassthrough(t *testing.T) {
	fc := newFilterContext(&Filter{})
	if fc.active {
		t.Fatal("empty filter should not be active")
	}
	pool := CurvePool{Address: "0xabc", UsdTotal: 0, IsBroken: true}
	if !fc.keep(pool) {
		t.Fatal("empty filter should keep every pool")
	}
}

func TestFilterContext_MinUSDTotal(t *testing.T) {
	fc := newFilterContext(&Filter{MinUSDTotal: 100_000})
	if !fc.active {
		t.Fatal("MinUSDTotal should make filter active")
	}
	if !fc.keep(CurvePool{Address: "0x1", UsdTotal: 150_000}) {
		t.Fatal("pool above threshold should be kept")
	}
	if fc.keep(CurvePool{Address: "0x2", UsdTotal: 99_999}) {
		t.Fatal("pool below threshold should be dropped")
	}
	if fc.keep(CurvePool{Address: "0x3", UsdTotal: 0}) {
		t.Fatal("zero-TVL pool should be dropped")
	}
	if got := fc.dropCounts[filterReasonTVL]; got != 2 {
		t.Fatalf("expected 2 TVL drops, got %d", got)
	}
}

func TestFilterContext_WhitelistAdditive(t *testing.T) {
	// whitelist narrows the set, but other predicates still apply
	fc := newFilterContext(&Filter{
		Whitelist:   []string{"0xAAA", "0xBBB"},
		MinUSDTotal: 1_000,
	})
	if !fc.keep(CurvePool{Address: "0xaaa", UsdTotal: 5_000}) {
		t.Fatal("whitelisted + above TVL should be kept")
	}
	if fc.keep(CurvePool{Address: "0xccc", UsdTotal: 5_000}) {
		t.Fatal("non-whitelisted should be dropped")
	}
	if fc.keep(CurvePool{Address: "0xbbb", UsdTotal: 500}) {
		t.Fatal("whitelisted but below TVL should still be dropped")
	}
	if got := fc.dropCounts[filterReasonWhitelist]; got != 1 {
		t.Fatalf("expected 1 whitelist drop, got %d", got)
	}
	if got := fc.dropCounts[filterReasonTVL]; got != 1 {
		t.Fatalf("expected 1 TVL drop, got %d", got)
	}
}

func TestFilterContext_DropBrokenAndRequireGauge(t *testing.T) {
	fc := newFilterContext(&Filter{DropBroken: true, RequireGauge: true})
	if fc.keep(CurvePool{Address: "0x1", IsBroken: true, GaugeAddress: "0xgauge"}) {
		t.Fatal("broken pool should be dropped")
	}
	if fc.keep(CurvePool{Address: "0x2", GaugeAddress: ""}) {
		t.Fatal("pool with no gauge should be dropped when RequireGauge=true")
	}
	if !fc.keep(CurvePool{Address: "0x3", GaugeAddress: "0xgauge"}) {
		t.Fatal("healthy + gauged pool should be kept")
	}
	if got := fc.dropCounts[filterReasonBroken]; got != 1 {
		t.Fatalf("expected 1 broken drop, got %d", got)
	}
	if got := fc.dropCounts[filterReasonGauge]; got != 1 {
		t.Fatalf("expected 1 gauge drop, got %d", got)
	}
}

func TestFilterContext_MinAge(t *testing.T) {
	now := time.Now().Unix()
	fc := newFilterContext(&Filter{MinAgeSeconds: 3600})
	if fc.keep(CurvePool{Address: "0x1", CreationTs: now - 1000}) {
		t.Fatal("pool younger than min age should be dropped")
	}
	if !fc.keep(CurvePool{Address: "0x2", CreationTs: now - 7200}) {
		t.Fatal("old-enough pool should be kept")
	}
	// pool with unknown creationTs (0) is treated as passing the age check
	if !fc.keep(CurvePool{Address: "0x3", CreationTs: 0}) {
		t.Fatal("pool with unknown CreationTs should pass the age predicate")
	}
	if got := fc.dropCounts[filterReasonAge]; got != 1 {
		t.Fatalf("expected 1 age drop, got %d", got)
	}
}

func TestFilterContext_MinVolumeWithProvider(t *testing.T) {
	provider := &stubVolumeProvider{
		volumes: map[string]float64{
			"0x1": 50_000,
			"0x2": 500,
		},
	}
	fc := newFilterContext(&Filter{MinVolumeUSD: 10_000, VolumeProvider: provider})
	if !fc.keep(CurvePool{Address: "0x1"}) {
		t.Fatal("pool with volume above threshold should be kept")
	}
	if fc.keep(CurvePool{Address: "0x2"}) {
		t.Fatal("pool with volume below threshold should be dropped")
	}
	if fc.keep(CurvePool{Address: "0xunknown"}) {
		t.Fatal("pool not present in volume map should be dropped (fail-closed)")
	}
	if got := fc.dropCounts[filterReasonVolume]; got != 2 {
		t.Fatalf("expected 2 volume drops, got %d", got)
	}
}

func TestFilterContext_MinVolumeMissingProvider(t *testing.T) {
	fc := newFilterContext(&Filter{MinVolumeUSD: 10_000})
	if !fc.missingProvider {
		t.Fatal("missingProvider flag should be set when MinVolumeUSD>0 and VolumeProvider nil")
	}
	// predicate should be skipped entirely — pool passes regardless of unknown volume
	if !fc.keep(CurvePool{Address: "0x1"}) {
		t.Fatal("with nil provider, volume predicate must be skipped (fail-open on misconfig)")
	}
	if fc.dropCounts[filterReasonVolume] != 0 {
		t.Fatal("no volume drops should be recorded when provider is nil")
	}
}

func TestFilterContext_WhitelistLowercased(t *testing.T) {
	fc := newFilterContext(&Filter{Whitelist: []string{"0xDEADBEEF"}})
	// whitelist entries and pool addresses compared case-insensitively
	if !fc.keep(CurvePool{Address: "0xdeadbeef"}) {
		t.Fatal("whitelist comparison should be case-insensitive")
	}
	if !fc.keep(CurvePool{Address: "0xDeAdBeEf"}) {
		t.Fatal("whitelist comparison should be case-insensitive (mixed case)")
	}
}
