// Package indexer reads the proxy's on-chain data from the Flare C-chain indexer database.
package indexer

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"gorm.io/gorm"

	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

// Source answers on-chain queries from the C-chain indexer database.
type Source struct {
	db        *gorm.DB
	addresses config.Addresses
	sync      database.SyncParams
}

var (
	_ policy.Source      = (*Source)(nil)
	_ machinepath.Source = (*Source)(nil)
)

// New returns a Source reading the contracts at addresses from db; sync bounds WaitSynced.
func New(db *gorm.DB, addresses config.Addresses, sync database.SyncParams) *Source {
	return &Source{db: db, addresses: addresses, sync: sync}
}

// LatestBlockHash returns the hash of the newest indexed block.
func (s *Source) LatestBlockHash(ctx context.Context) (common.Hash, error) {
	block, err := database.FetchLatestBlock(ctx, s.db, nil)
	if err != nil {
		return common.Hash{}, err
	}

	return common.HexToHash(block.Hash), nil
}

// Head returns the number and timestamp of the last indexed block.
func (s *Source) Head(ctx context.Context) (uint64, uint64, error) {
	state, err := database.FetchState(ctx, s.db, nil)
	if err != nil {
		return 0, 0, err
	}

	return state.Index, state.BlockTimestamp, nil
}

// WaitSynced blocks until the indexer has caught up with the chain.
func (s *Source) WaitSynced(ctx context.Context) error {
	return database.WaitCIndexerToSync(ctx, s.db, s.sync, logger.Logger())
}
