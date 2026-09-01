package policy

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type stubNodeState struct{ id uint32 }

func (s stubNodeState) LastAppliedPolicyID() uint32 { return s.id }

func restartTeeInfo(lastID uint32) *types.TeeInfoResponse {
	tir := &types.TeeInfoResponse{}
	tir.TeeInfo.InitialSigningPolicyHash = common.HexToHash("0x01")
	tir.TeeInfo.LastSigningPolicyID = lastID
	return tir
}

func policyDB(t *testing.T, relayAddr common.Address, epochs []uint32) *gorm.DB {
	t.Helper()

	db, _ := testutil.InMemoryDB(t, "policyRestart")
	require.NoError(t, db.AutoMigrate(&database.Log{}))
	for j, e := range epochs {
		testutil.InsertSigningPolicyLog(t, db, relayAddr, e, uint64(100+j))
	}
	return db
}

func TestInitializeRestart(t *testing.T) {
	const epoch = uint32(5000)

	tests := []struct {
		name       string
		dbEpochs   []uint32
		lastID     uint32
		offset     int
		wantErr    bool
		wantEpochs []uint32
	}{
		{
			name:       "offset zero loads only last",
			dbEpochs:   []uint32{epoch - 1, epoch},
			lastID:     epoch,
			offset:     0,
			wantEpochs: []uint32{epoch},
		},
		{
			name:       "backfills offset+1 policies",
			dbEpochs:   []uint32{epoch - 2, epoch - 1, epoch},
			lastID:     epoch,
			offset:     2,
			wantEpochs: []uint32{epoch - 2, epoch - 1, epoch},
		},
		{
			// a source-bound relay's history starts after startID
			name:       "missing history is skipped",
			dbEpochs:   []uint32{epoch - 1, epoch},
			lastID:     epoch,
			offset:     5,
			wantEpochs: []uint32{epoch - 1, epoch},
		},
		{
			name:     "last policy missing is an error",
			dbEpochs: []uint32{epoch - 1},
			lastID:   epoch,
			offset:   1,
			wantErr:  true,
		},
		{
			// epoch 0 is unqueryable once newer logs exist (zero-hash topic), so
			// the clamped walk-back skips it instead of loading a wrong policy
			name:       "offset larger than lastID clamps at epoch zero",
			dbEpochs:   []uint32{0, 1},
			lastID:     1,
			offset:     5,
			wantEpochs: []uint32{1},
		},
		{
			name:       "epoch zero loads only itself",
			dbEpochs:   []uint32{0},
			lastID:     0,
			offset:     3,
			wantEpochs: []uint32{0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			relayAddr := common.HexToAddress("0x1111")
			db := policyDB(t, relayAddr, tc.dbEpochs)

			s := NewService(nil, nil, config.Addresses{Relay: relayAddr}, 14, stubNodeState{}, nil)

			err := s.Initialize(context.Background(), db, tc.offset, restartTeeInfo(tc.lastID))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			require.NotNil(t, s.activePolicy)
			require.Equal(t, tc.lastID, s.activePolicy.RewardEpochID)

			got := make([]uint32, 0, len(s.restartPolicies))
			for _, p := range s.restartPolicies {
				got = append(got, p.RewardEpochID)
			}
			require.Equal(t, tc.wantEpochs, got)
		})
	}
}

// TestRunEmitsRestartBackfill guards the channel sizing: Run sends the whole
// backfill before a consumer exists, so an undersized buffer deadlocks it.
func TestRunEmitsRestartBackfill(t *testing.T) {
	const (
		lastID = uint32(100)
		offset = 5
	)

	relayAddr := common.HexToAddress("0x1111")
	epochs := make([]uint32, 0, offset+1)
	for id := lastID - offset; id <= lastID; id++ {
		epochs = append(epochs, id)
	}
	db := policyDB(t, relayAddr, epochs)

	s := NewService(nil, nil, config.Addresses{Relay: relayAddr}, 14, stubNodeState{}, nil)
	require.NoError(t, s.Initialize(context.Background(), db, offset, restartTeeInfo(lastID)))

	ch, err := s.Run(t.Context(), db, time.Minute)
	require.NoError(t, err)

	for _, want := range epochs {
		p := <-ch
		require.Equal(t, want, p.RewardEpochID)
	}
}
