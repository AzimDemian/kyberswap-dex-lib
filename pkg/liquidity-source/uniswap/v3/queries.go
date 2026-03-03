package uniswapv3

import (
	"bytes"
	"text/template"
)

type DiscoveryPoolsListQueryParams struct {
	AllowSubgraphError bool
	First              int
	Skip               int
	MinTVLUSD          float64
	MinVolumeUSD       float64
}

type PoolTicksQueryParams struct {
	AllowSubgraphError bool
	PoolAddress        string
	LastTickIdx        string
}

func getDiscoveryPoolsListQuery(allowSubgraphError bool, first, skip int, minTVLUSD, minVolumeUSD float64) string {
	var tpl bytes.Buffer
	td := DiscoveryPoolsListQueryParams{
		AllowSubgraphError: allowSubgraphError,
		First:              first,
		Skip:               skip,
		MinTVLUSD:          minTVLUSD,
		MinVolumeUSD:       minVolumeUSD,
	}

	t, err := template.New("discoveryPoolsListQuery").Parse(`{
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
			feeTier
			liquidity
			sqrtPrice
			tick
			createdAtTimestamp
			totalValueLockedUSD
			volumeUSD
			token0 {
				id
				name
				symbol
				decimals
			}
			token1 {
				id
				name
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
		allowSubgraphError,
		poolAddress,
		lastTickIdx,
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
