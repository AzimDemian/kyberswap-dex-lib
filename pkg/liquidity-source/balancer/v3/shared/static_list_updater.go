package shared

import (
	"context"

	entity "github.com/KyberNetwork/kyberswap-dex-lib/pkg/entity"
)

// StaticPoolsListUpdater returns a fixed set of pre-built entity.Pool objects on the
// first GetNewPools call, then signals completion on all subsequent calls.
// Used to seed the bootstrap from a snapshot file without hitting the subgraph.
type StaticPoolsListUpdater struct {
	pools []entity.Pool
	done  bool
}

func NewStaticPoolsListUpdater(pools []entity.Pool) *StaticPoolsListUpdater {
	return &StaticPoolsListUpdater{pools: pools}
}

func (u *StaticPoolsListUpdater) GetNewPools(_ context.Context, meta []byte) ([]entity.Pool, []byte, error) {
	if u.done || len(u.pools) == 0 {
		return nil, nil, nil
	}
	u.done = true
	return u.pools, meta, nil
}
