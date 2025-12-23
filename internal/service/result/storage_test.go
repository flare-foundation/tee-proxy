package result

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/random"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"github.com/stretchr/testify/require"
)

func TestWaitOnResponse(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	s := NewStorage(c)

	t.Run("already stored", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponse(t, actionID)

		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		retrievedRes, err := s.WaitOnResponse(t.Context(), actionID, types.Submit, 0)
		require.NoError(t, err)

		require.Equal(t, res, retrievedRes)
	})

	t.Run("stored after waiting", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponse(t, actionID)

		var wg sync.WaitGroup

		start := time.Now()

		wg.Go(func() {
			retrievedRes, err := s.WaitOnResponse(t.Context(), actionID, types.Submit, 0)
			require.NoError(t, err)
			require.Equal(t, res, retrievedRes)
		})

		time.Sleep(100 * time.Millisecond)

		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		wg.Wait()

		require.Less(t, time.Since(start), 110*time.Millisecond)
	})

	t.Run("not stored", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		var wg sync.WaitGroup

		ctx, cancel := context.WithCancel(t.Context())
		wg.Go(func() {
			_, err := s.WaitOnResponse(ctx, actionID, types.Submit, 0)
			require.Error(t, err)
		})

		cancel()

		wg.Wait()
	})

	t.Run("timeout", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		var wg sync.WaitGroup

		start := time.Now()

		wg.Go(func() {
			_, err := s.WaitOnResponse(t.Context(), actionID, types.Submit, 10*time.Millisecond)
			require.Error(t, err)
		})

		wg.Wait()
		require.Less(t, time.Since(start), 100*time.Millisecond)
	})
}

func createMockResponse(t *testing.T, id common.Hash) *types.ActionResponse {
	t.Helper()
	opType, err := convert.StringToCommonHash("MOCKT")
	require.NoError(t, err)

	opCommand, err := convert.StringToCommonHash("MOCKC")
	require.NoError(t, err)

	return &types.ActionResponse{
		Result: types.ActionResult{
			ID:                     id,
			SubmissionTag:          types.Submit,
			Status:                 1,
			Log:                    "",
			OPType:                 opType,
			OPCommand:              opCommand,
			AdditionalResultStatus: hexutil.Bytes{},
			Version:                "",
			Data:                   []byte("mock data"),
		},
		Signature:      hexutil.Bytes{},
		ProxySignature: hexutil.Bytes{},
	}
}
