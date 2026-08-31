package policy

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/stretchr/testify/require"
)

type stubNodeState struct{ id uint32 }

func (s stubNodeState) LastAppliedPolicyID() uint32 { return s.id }

func TestInitializeRestart(t *testing.T) {
	const epoch = uint32(5000)

	tests := []struct {
		name       string
		dbEpochs   []uint32
		lastID     uint32
		wantErr    bool
		wantEpochs []uint32
	}{
		{
			// a source-bound relay's history starts at lastID
			name:       "previous policy missing falls back to last only",
			dbEpochs:   []uint32{epoch},
			lastID:     epoch,
			wantEpochs: []uint32{epoch, epoch},
		},
		{
			name:       "both policies present",
			dbEpochs:   []uint32{epoch - 1, epoch},
			lastID:     epoch,
			wantEpochs: []uint32{epoch - 1, epoch},
		},
		{
			name:     "last policy missing is an error",
			dbEpochs: nil,
			lastID:   epoch,
			wantErr:  true,
		},
		{
			name:       "epoch zero loads only itself",
			dbEpochs:   []uint32{0},
			lastID:     0,
			wantEpochs: []uint32{0, 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := testutil.InMemoryDB(t, "policyRestart")
			require.NoError(t, db.AutoMigrate(&database.Log{}))

			relayAddr := common.HexToAddress("0x1111")
			for j, e := range tc.dbEpochs {
				testutil.InsertSigningPolicyLog(t, db, relayAddr, e, uint64(100+j))
			}

			s := NewService(nil, nil, config.Addresses{Relay: relayAddr}, 14, stubNodeState{}, nil)

			tir := &types.TeeInfoResponse{}
			tir.TeeInfo.InitialSigningPolicyHash = common.HexToHash("0x01")
			tir.TeeInfo.LastSigningPolicyID = tc.lastID

			err := s.Initialize(context.Background(), db, 0, tir)
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
