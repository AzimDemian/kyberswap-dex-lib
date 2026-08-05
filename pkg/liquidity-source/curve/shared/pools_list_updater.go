package shared

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KyberNetwork/blockchain-toolkit/time/durationjson"
	"github.com/KyberNetwork/ethrpc"
	"github.com/KyberNetwork/logger"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/pkg/errors"
	"github.com/samber/lo"

	"github.com/KyberNetwork/kyberswap-dex-lib/pkg/valueobject"
)

type (
	PoolsListUpdater struct {
		config       *Config
		client       *resty.Client
		ethrpcClient *ethrpc.Client
		logger       logger.Logger
	}

	Config struct {
		DexID       string              `mapstructure:"dexID" json:"dexID,omitempty"`
		ChainCode   string              `mapstructure:"chain_code" json:"chain_code,omitempty"`
		ChainID     valueobject.ChainID `mapstructure:"chain_id" json:"chain_id,omitempty"`
		HTTPConfig  HTTPConfig          `mapstructure:"http_config" json:"http_config"`
		DataSources []CurveDataSource   `mapstructure:"data_sources" json:"data_sources,omitempty"`

		FetchPoolsMinDuration durationjson.Duration `mapstructure:"fetch_pools_min_duration" json:"fetch_pools_min_duration"`

		Filter Filter `mapstructure:"filter" json:"filter,omitempty"`
	}

	HTTPConfig struct {
		BaseURL    string                `mapstructure:"base_url" json:"base_url,omitempty"`
		Timeout    durationjson.Duration `mapstructure:"timeout" json:"timeout"`
		RetryCount int                   `mapstructure:"retry_count" json:"retry_count,omitempty"`
	}

	// Filter restricts which Curve pools survive discovery, applied inside
	// GetNewPools before any per-type initPools RPC hydration happens.
	// A zero-valued Filter is a pass-through: all pools survive.
	Filter struct {
		Whitelist     []string `mapstructure:"whitelist" json:"whitelist,omitempty"`
		MinUSDTotal   float64  `mapstructure:"min_usd_total" json:"min_usd_total,omitempty"`
		MinVolumeUSD  float64  `mapstructure:"min_volume_usd" json:"min_volume_usd,omitempty"`
		DropBroken    bool     `mapstructure:"drop_broken" json:"drop_broken,omitempty"`
		RequireGauge  bool     `mapstructure:"require_gauge" json:"require_gauge,omitempty"`
		MinAgeSeconds int64    `mapstructure:"min_age_seconds" json:"min_age_seconds,omitempty"`

		// VolumeProvider is set programmatically by the caller (not from config).
		// If MinVolumeUSD > 0 and VolumeProvider is nil, the volume predicate is
		// logged once per run and skipped (fail-open on misconfiguration).
		VolumeProvider VolumeProvider `mapstructure:"-" json:"-"`
	}

	// VolumeProvider returns 24h volume in USD for a pool address.
	// Implementations must be safe for concurrent use.
	VolumeProvider interface {
		GetVolume(address string) (volumeUSD float64, ok bool)
	}
)

func NewPoolsListUpdater(config *Config, ethrpcClient *ethrpc.Client, logger logger.Logger) *PoolsListUpdater {
	client := resty.NewWithClient(lo.ToPtr(lo.FromPtr(http.DefaultClient))).
		SetBaseURL(config.HTTPConfig.BaseURL).
		SetTimeout(config.HTTPConfig.Timeout.Duration).
		SetRetryCount(config.HTTPConfig.RetryCount)

	return &PoolsListUpdater{
		config:       config,
		client:       client,
		ethrpcClient: ethrpcClient,
		logger:       logger,
	}
}

func (u *PoolsListUpdater) GetNewPools(ctx context.Context, metadataBytes []byte, poolTypeSet mapset.Set[CurvePoolType]) ([]CurvePoolWithType, []byte, error) {
	var pools []CurvePoolWithType

	// pool list doesn't get changed often, so only fetch after some minutes
	now := time.Now().UTC()
	var metadata PoolListUpdaterMetadata
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			u.logger.WithFields(logger.Fields{
				"error": err,
			}).Error("failed to unmarshal metadataBytes")
			return nil, nil, err
		}

		if now.Sub(metadata.LastRun) < u.config.FetchPoolsMinDuration.Duration {
			u.logger.Debugf("skip fetching new pool %v %v", now, metadata.LastRun)
			return nil, metadataBytes, nil
		}
	}
	metadata.LastRun = now

	fc := newFilterContext(&u.config.Filter)
	if fc.missingProvider {
		u.logger.Warnf("curve filter: MinVolumeUSD=%v set but VolumeProvider is nil, volume predicate skipped",
			u.config.Filter.MinVolumeUSD)
	}

	typeCount := map[CurvePoolType]int{}

	for _, dataSource := range u.config.DataSources {
		rawPools, err := u.GetNewPoolsFromDataSource(ctx, dataSource)
		if err != nil {
			return nil, nil, err
		}

		typeMap, err := u.ClassifyPools(ctx, dataSource, rawPools)
		if err != nil {
			return nil, nil, err
		}

		for _, rawPool := range rawPools {
			poolType, ok := typeMap[rawPool.Address]
			if !ok {
				u.logger.Debugf("unknown Curve pool type %s", rawPool.Address)
				continue
			}
			typeCount[poolType] += 1

			if !poolTypeSet.Contains(poolType) {
				u.logger.Debugf("ignore Curve pool type %s %s", poolType, rawPool.Address)
				continue
			}

			if !fc.keep(rawPool) {
				continue
			}

			pools = append(pools, CurvePoolWithType{
				CurvePool: rawPool,
				PoolType:  poolType,
			})
		}
	}
	if fc.active {
		u.logger.Infof("curve filter: kept %d pools (dropped: whitelist=%d broken=%d gauge=%d age=%d tvl=%d vol=%d)",
			len(pools),
			fc.dropCounts[filterReasonWhitelist],
			fc.dropCounts[filterReasonBroken],
			fc.dropCounts[filterReasonGauge],
			fc.dropCounts[filterReasonAge],
			fc.dropCounts[filterReasonTVL],
			fc.dropCounts[filterReasonVolume],
		)
	}
	u.logger.Infof("fetched %d pools, raw type count: %v", len(pools), typeCount)

	return pools, metadata.ToBytes(), nil
}

// filterReason values are used as keys into filterContext.dropCounts.
type filterReason int

const (
	filterReasonWhitelist filterReason = iota
	filterReasonBroken
	filterReasonGauge
	filterReasonAge
	filterReasonTVL
	filterReasonVolume
)

// filterContext carries precomputed state (whitelist set, "now", per-run warning
// flags) for the Filter predicates so they can be evaluated cheaply per pool.
type filterContext struct {
	f               *Filter
	active          bool
	whitelist       map[string]struct{} // nil if no whitelist
	provider        VolumeProvider
	missingProvider bool
	now             int64
	dropCounts      map[filterReason]int
}

func newFilterContext(f *Filter) *filterContext {
	fc := &filterContext{
		f:          f,
		provider:   f.VolumeProvider,
		now:        time.Now().Unix(),
		dropCounts: map[filterReason]int{},
	}
	if len(f.Whitelist) > 0 {
		fc.whitelist = make(map[string]struct{}, len(f.Whitelist))
		for _, w := range f.Whitelist {
			fc.whitelist[strings.ToLower(w)] = struct{}{}
		}
		fc.active = true
	}
	if f.DropBroken || f.RequireGauge || f.MinUSDTotal > 0 || f.MinAgeSeconds > 0 {
		fc.active = true
	}
	if f.MinVolumeUSD > 0 {
		fc.active = true
		if f.VolumeProvider == nil {
			fc.missingProvider = true
		}
	}
	return fc
}

// keep returns true if the pool passes all filter predicates. Drop counts are
// accumulated by reason for end-of-run reporting.
func (fc *filterContext) keep(pool CurvePool) bool {
	f := fc.f
	if fc.whitelist != nil {
		if _, ok := fc.whitelist[strings.ToLower(pool.Address)]; !ok {
			fc.dropCounts[filterReasonWhitelist]++
			return false
		}
	}
	if f.DropBroken && pool.IsBroken {
		fc.dropCounts[filterReasonBroken]++
		return false
	}
	if f.RequireGauge && pool.GaugeAddress == "" {
		fc.dropCounts[filterReasonGauge]++
		return false
	}
	if f.MinAgeSeconds > 0 && pool.CreationTs > 0 && fc.now-pool.CreationTs < f.MinAgeSeconds {
		fc.dropCounts[filterReasonAge]++
		return false
	}
	if f.MinUSDTotal > 0 && pool.UsdTotal < f.MinUSDTotal {
		fc.dropCounts[filterReasonTVL]++
		return false
	}
	if f.MinVolumeUSD > 0 && fc.provider != nil {
		vol, ok := fc.provider.GetVolume(pool.Address)
		if !ok || vol < f.MinVolumeUSD {
			fc.dropCounts[filterReasonVolume]++
			return false
		}
	}
	return true
}

func (u *PoolsListUpdater) GetNewPoolsFromDataSource(ctx context.Context, dataSource CurveDataSource) ([]CurvePool, error) {
	u.logger.Infof("fetching pool from %s", dataSource)
	req := u.client.R().SetContext(ctx)

	var result GetPoolsResult

	resp, err := req.SetResult(&result).Get(fmt.Sprintf(getPoolsEndpoint, u.config.ChainCode, dataSource))
	if err != nil {
		return nil, err
	}

	if !resp.IsSuccess() || !result.Success {
		return nil, errors.WithMessagef(ErrGetPoolsFailed, "[curve] response status: %v, response error: %v, result status %v", resp.Status(), resp.Error(), result.Success)
	}

	// normalize
	for i := range result.Data.PoolData {
		result.Data.PoolData[i].Address = strings.ToLower(result.Data.PoolData[i].Address)
		for j := range result.Data.PoolData[i].Coins {
			if strings.EqualFold(result.Data.PoolData[i].Coins[j].Address, valueobject.NativeAddress) {
				result.Data.PoolData[i].Coins[j].Address = strings.ToLower(valueobject.WrappedNativeMap[u.config.ChainID])
				result.Data.PoolData[i].Coins[j].IsOrgNative = true
			}
		}
	}
	u.logger.Infof("fetched %d pool from %s", len(result.Data.PoolData), dataSource)

	return result.Data.PoolData, nil
}
