package policy

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestFetchSigningPolicy(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "fetchPolicy")
	require.NoError(t, db.AutoMigrate(&database.Log{}))

	relayAddr := common.HexToAddress("0x2222")
	testutil.InsertSigningPolicyLog(t, db, relayAddr, 7, 100)

	p, found, err := FetchSigningPolicy(context.Background(), db, relayAddr, 7)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(7), p.RewardEpochID)

	p, found, err = FetchSigningPolicy(context.Background(), db, relayAddr, 6)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, p)
}
