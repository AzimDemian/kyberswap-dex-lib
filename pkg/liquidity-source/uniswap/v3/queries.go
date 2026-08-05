package uniswapv3

import (
	"bytes"
	"fmt"
	"math/big"
	"strings"
	"text/template"
)

type PoolsListQueryParams struct {
	AllowSubgraphError     bool
	LastCreatedAtTimestamp *big.Int
	First                  int
	Skip                   int
}

func getPoolsListQuery(allowSubgraphError bool, lastCreatedAtTimestamp *big.Int, first, skip int) string {
	var tpl bytes.Buffer
	td := PoolsListQueryParams{
		AllowSubgraphError:     allowSubgraphError,
		LastCreatedAtTimestamp: lastCreatedAtTimestamp,
		First:                  first,
		Skip:                   skip,
	}

	t, err := template.New("poolsListQuery").Parse(`{
		pools(
			{{ if .AllowSubgraphError }}subgraphError: allow,{{ end }}
			where: {
				createdAtTimestamp_gte: {{ .LastCreatedAtTimestamp }}
			},
			first: {{ .First }},
			skip: {{ .Skip }},
			orderBy: createdAtTimestamp,
			orderDirection: asc
		) {
			id
			liquidity
			sqrtPrice
			createdAtTimestamp
			tick
			feeTier
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

	if err = t.Execute(&tpl, td); err != nil {
		panic(err)
	}

	return tpl.String()
}

type PoolTicksQueryParams struct {
	AllowSubgraphError bool
	PoolAddress        string
	LastTickIdx        string
}

func getPoolTicksQuery(allowSubgraphError bool, poolAddress, lastTickIdx string) string {
	var tpl bytes.Buffer
	td := PoolTicksQueryParams{
		AllowSubgraphError: allowSubgraphError,
		PoolAddress:        poolAddress,
		LastTickIdx:        lastTickIdx,
	}

	t := template.Must(template.New("poolTicksQuery").Parse(`{
		ticks(
			{{ if .AllowSubgraphError }}subgraphError: allow,{{ end }}
			where: {
				pool: "{{ .PoolAddress }}"
				{{ if .LastTickIdx }}tickIdx_gt: {{ .LastTickIdx }},{{ end }}
				liquidityGross_not: 0
			},
			orderBy: tickIdx,
			orderDirection: asc,
			first: 1000
		) {
			tickIdx
			liquidityGross
			liquidityNet
		}
	}`))

	_ = t.Execute(&tpl, td)
	return tpl.String()
}

// getPoolsByAddressesQuery builds a GraphQL query that fetches specific pools by address
// using the standard id_in filter supported by The Graph. Used for subgraph fallback
// when RPC metadata fetch fails for individual pools.
func getPoolsByAddressesQuery(addresses []string) string {
	quoted := make([]string, len(addresses))
	for i, a := range addresses {
		quoted[i] = fmt.Sprintf("%q", strings.ToLower(a))
	}
	return fmt.Sprintf(`{
		pools(where: { id_in: [%s] }, first: %d) {
			id
			feeTier
			createdAtTimestamp
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
	}`, strings.Join(quoted, ", "), len(addresses))
}
