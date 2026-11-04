package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"github.com/flare-foundation/tee-proxy/internal/chain/indexer"
	"github.com/flare-foundation/tee-proxy/internal/chain/jsonrpc"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

// chainSource is where the proxy reads on-chain data from.
type chainSource interface {
	policy.Source
	machinepath.Source
	LatestBlockHash(ctx context.Context) (common.Hash, error)
	WaitSynced(ctx context.Context) error
	Close()
}

var (
	_ chainSource = (*indexer.Source)(nil)
	_ chainSource = (*jsonrpc.Source)(nil)
)

// openChain connects to the chain source cfg selects.
func openChain(ctx context.Context, cfg config.Proxy) (chainSource, error) {
	sync := database.SyncParams{
		Retries:            30,
		MaxSleepTime:       cfg.DBSyncMaxSleepTime,
		OutOfSyncTolerance: outOfSyncTolerance,
		MinSleepTime:       1 * time.Second,
	}

	if cfg.Chain.RPC() {
		if cfg.DB.Host != "" {
			logger.Warn("chain source is rpc: the [db] section is ignored")
		}
		// voters and weights of an INITIALIZE_POLICY come from the endpoint unverified
		logger.Warn("chain source is a JSON-RPC endpoint: use a node you trust; public endpoints are for testing only")

		src, err := jsonrpc.Dial(ctx, cfg.Chain, cfg.Addresses, cfg.ChainID, sync)
		if err != nil {
			return nil, fmt.Errorf("connecting to chain node: %w", err)
		}
		return src, nil
	}

	database.SetErrorLogger(logger.Logger())
	db, err := database.Connect(&cfg.DB)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	return indexer.New(db, cfg.Addresses, sync), nil
}
