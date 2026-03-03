package v3

import (
	"bytes"
	"text/template"

	uniswapv3 "github.com/KyberNetwork/kyberswap-dex-lib/pkg/liquidity-source/uniswap/v3"
)

type (
	DiscoveryPoolsListQueryParams struct {
		AllowSubgraphError bool
		First              int
		Skip               int
		MinTVLUSD          float64
		MinVolumeUSD       float64
	}
	PoolTicksQueryParams = uniswapv3.PoolTicksQueryParams
)

func getDiscoveryPoolsListQuery(allowSubgraphError bool, first, skip int, minTVLUSD, minVolumeUSD float64) string {
	var tpl bytes.Buffer
	td := DiscoveryPoolsListQueryParams{
		AllowSubgraphError: allowSubgraphError,
		First:              first,
		Skip:               skip,
		MinTVLUSD:          minTVLUSD,
		MinVolumeUSD:       minVolumeUSD,
	}

	t, err := template.New("poolsListQuery").Parse(`{
		pools(
			{{ if .AllowSubgraphError }}subgraphError: allow,{{ end }}
			first: {{ .First }},
			skip: {{ .Skip }},
			where: {
				liquidity_not: "0"
				{{ if gt .MinTVLUSD 0.0 }}totalValueLockedUSD_gt: {{ .MinTVLUSD }},{{ end }}
				{{ if gt .MinVolumeUSD 0.0 }}volumeUSD_gt: {{ .MinVolumeUSD }},{{ end }}
			},
			orderBy: totalValueLockedUSD,
			orderDirection: desc
		) {
			id
			liquidity
			sqrtPrice
			createdAtTimestamp
			tick
			feeTier
			token0 {
				id
				symbol
				decimals
			}
			token1 {
				id
				symbol
				decimals
			}
		}
	}`)

	if err != nil {
		panic(err)
	}

	err = t.Execute(&tpl, td)

	if err != nil {
		panic(err)
	}

	return tpl.String()
}

func getPoolTicksQuery(allowSubgraphError bool, poolAddress string, lastTickIdx string) string {
	var tpl bytes.Buffer
	td := PoolTicksQueryParams{
		AllowSubgraphError: allowSubgraphError,
		PoolAddress:        poolAddress,
		LastTickIdx:        lastTickIdx,
	}

	t, err := template.New("poolTicksQuery").Parse(`{
		ticks(
			{{ if .AllowSubgraphError }}subgraphError: allow,{{ end }}
			where: {
				pool: "{{.PoolAddress}}"
				{{ if .LastTickIdx }}tickIdx_gt: {{.LastTickIdx}},{{ end }}
				liquidityGross_not: 0
			},
			orderBy: tickIdx,
			orderDirection: asc,
			first: 1000
		) {
			tickIdx
			liquidityNet
			liquidityGross
		}
	}`)

	if err != nil {
		panic(err)
	}

	err = t.Execute(&tpl, td)

	if err != nil {
		panic(err)
	}

	return tpl.String()
}
