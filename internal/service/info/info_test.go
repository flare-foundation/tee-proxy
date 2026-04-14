package info

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/testutil"

	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/storage"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/stretchr/testify/require"
)

func TestInsertBlock(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "choose")
	err := db.AutoMigrate(&database.Block{})
	require.NoError(t, err)

	var latestBlockHash common.Hash
	for i := uint64(1); i <= 3; i++ {
		block, hash := testutil.CreateBlock(fmt.Sprintf("%d", i), i)
		latestBlockHash = hash
		err = db.Create(block).Error
		require.NoError(t, err)
	}

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c))

	s := NewService(db, aq, rs, &config.InfoTiming{
		CycleInternal:          10 * time.Millisecond,
		CycleQueueResponseWait: 1 * time.Second,
	})

	go func() {
		err := s.Run(t.Context())
		require.Error(t, err)
	}()

	var a *types.Action
	require.Eventually(t, func() bool {
		var err error
		a, err = aq.Dequeue(t.Context(), processorutils.Direct)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, types.Submit, a.Data.SubmissionTag)
	require.Equal(t, types.Direct, a.Data.Type)

	var instruction types.DirectInstruction
	err = json.Unmarshal(a.Data.Message, &instruction)
	require.NoError(t, err)
	require.Equal(t, op.Get.Hash(), instruction.OPType)
	require.Equal(t, op.TEEInfo.Hash(), instruction.OPCommand)

	var data types.TeeInfoRequest
	err = json.Unmarshal(instruction.Message, &data)
	require.NoError(t, err)
	require.Equal(t, data.Challenge, latestBlockHash)

	resp := &types.TeeInfoResponse{
		TeeInfo: types.TeeInfo{
			Challenge: latestBlockHash,
			State:     types.TeeState{},
		},
		Attestation: hexutil.Bytes{},
	}
	m, err := json.Marshal(resp)
	require.NoError(t, err)

	ar := &types.ActionResponse{
		Result: types.ActionResult{
			ID:            a.Data.ID,
			SubmissionTag: a.Data.SubmissionTag,
			Status:        1,
			OPType:        op.Get.Hash(),
			OPCommand:     op.TEEInfo.Hash(),
			Version:       "1.0.0",
			Data:          m,
		},
	}

	require.NoError(t, err)
	err = rs.StoreResponse(t.Context(), ar)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		s.RLock()
		defer s.RUnlock()
		return s.Latest.TeeInfo.Challenge == latestBlockHash
	}, 2*time.Second, 10*time.Millisecond)
}
