package indexer

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

var testRelay = common.HexToAddress("0x2222")

func TestFetchSigningPolicy(t *testing.T) {
	db := newDB(t)
	testutil.InsertSigningPolicyLog(t, db, testRelay, 7, 100)
	src := New(db, config.Addresses{Relay: testRelay}, database.SyncParams{})

	p, found, err := policy.FetchSigningPolicy(context.Background(), src, 7)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(7), p.RewardEpochID)

	p, found, err = policy.FetchSigningPolicy(context.Background(), src, 6)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, p)
}

// TestSigningPolicyEpochZero pins the indexer quirk: epoch 0's topic is the zero
// hash, which the query cannot filter on, so the newest event is answered and the
// shared epoch guard reports epoch 0 as not found.
func TestSigningPolicyEpochZero(t *testing.T) {
	db := newDB(t)
	testutil.InsertSigningPolicyLog(t, db, testRelay, 0, 100)
	testutil.InsertSigningPolicyLog(t, db, testRelay, 1, 101)
	src := New(db, config.Addresses{Relay: testRelay}, database.SyncParams{})

	event, found, err := src.SigningPolicy(context.Background(), 0)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(1), event.RewardEpochId.Uint64())
	require.Equal(t, uint64(101), event.Raw.BlockNumber)

	_, found, err = policy.FetchSigningPolicy(context.Background(), src, 0)
	require.NoError(t, err)
	require.False(t, found)
}

func TestHeadAndLatestBlockHash(t *testing.T) {
	db := newDB(t)
	testutil.InsertState(t, db, 120, 1_700_000_000)
	block, hash := testutil.CreateBlock("1", 1)
	require.NoError(t, db.Create(block).Error)
	src := New(db, config.Addresses{}, database.SyncParams{})

	number, timestamp, err := src.Head(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(120), number)
	require.Equal(t, uint64(1_700_000_000), timestamp)

	got, err := src.LatestBlockHash(context.Background())
	require.NoError(t, err)
	require.Equal(t, hash, got)
}
