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
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"github.com/stretchr/testify/require"
)

func TestStoreResponse(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	s := NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, 30*time.Minute)

	t.Run("store and retrieve", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.Submit, 1)

		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		got, err := s.FetchResponse(t.Context(), actionID, types.Submit)
		require.NoError(t, err)
		require.Equal(t, res, got)
	})

	t.Run("cannot override final status 0", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 0)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		override := createMockResponseWithStatus(t, actionID, types.End, 1)
		err = s.StoreResponse(t.Context(), override)
		require.Error(t, err)
		require.ErrorContains(t, err, "override final status")

		got, err := s.FetchResponse(t.Context(), actionID, types.End)
		require.NoError(t, err)
		require.Equal(t, uint8(0), got.Result.Status)
	})

	t.Run("cannot override final status 1", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 1)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		override := createMockResponseWithStatus(t, actionID, types.End, 5)
		err = s.StoreResponse(t.Context(), override)
		require.Error(t, err)
		require.ErrorContains(t, err, "override final status")

		got, err := s.FetchResponse(t.Context(), actionID, types.End)
		require.NoError(t, err)
		require.Equal(t, uint8(1), got.Result.Status)
	})

	t.Run("cannot override transient with equal status", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 3)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		override := createMockResponseWithStatus(t, actionID, types.End, 3)
		err = s.StoreResponse(t.Context(), override)
		require.Error(t, err)
		require.ErrorContains(t, err, "override higher transient status")
	})

	t.Run("cannot override transient with lower status", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 5)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		override := createMockResponseWithStatus(t, actionID, types.End, 3)
		err = s.StoreResponse(t.Context(), override)
		require.Error(t, err)
		require.ErrorContains(t, err, "override higher transient status")

		got, err := s.FetchResponse(t.Context(), actionID, types.End)
		require.NoError(t, err)
		require.Equal(t, uint8(5), got.Result.Status)
	})

	t.Run("can override transient with higher transient status", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 2)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		override := createMockResponseWithStatus(t, actionID, types.End, 4)
		err = s.StoreResponse(t.Context(), override)
		require.NoError(t, err)

		got, err := s.FetchResponse(t.Context(), actionID, types.End)
		require.NoError(t, err)
		require.Equal(t, uint8(4), got.Result.Status)
	})

	t.Run("can override transient with final status", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 5)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		override := createMockResponseWithStatus(t, actionID, types.End, 0)
		err = s.StoreResponse(t.Context(), override)
		require.NoError(t, err)

		got, err := s.FetchResponse(t.Context(), actionID, types.End)
		require.NoError(t, err)
		require.Equal(t, uint8(0), got.Result.Status)
	})

	t.Run("submit tag uses short TTL", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.Submit, 0)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		got, err := s.FetchResponse(t.Context(), actionID, types.Submit)
		require.NoError(t, err)
		require.Equal(t, res, got)
	})

	t.Run("end tag uses default TTL", func(t *testing.T) {
		actionID, err := random.Hash()
		require.NoError(t, err)

		res := createMockResponseWithStatus(t, actionID, types.End, 0)
		err = s.StoreResponse(t.Context(), res)
		require.NoError(t, err)

		got, err := s.FetchResponse(t.Context(), actionID, types.End)
		require.NoError(t, err)
		require.Equal(t, res, got)
	})
}

func TestWaitOnResponse(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	s := NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, 30*time.Minute)

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

		// The 100ms gap is part of the assertion: the final require.Less below proves
		// WaitOnResponse unblocked via pub/sub shortly after StoreResponse rather than
		// polling. Do not replace with require.Eventually.
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
	return createMockResponseWithStatus(t, id, types.Submit, 1)
}

func createMockResponseWithStatus(t *testing.T, id common.Hash, tag types.SubmissionTag, status uint8) *types.ActionResponse {
	t.Helper()
	opType, err := convert.StringToCommonHash("MOCKT")
	require.NoError(t, err)

	opCommand, err := convert.StringToCommonHash("MOCKC")
	require.NoError(t, err)

	return &types.ActionResponse{
		Result: types.ActionResult{
			ID:                     id,
			SubmissionTag:          tag,
			Status:                 status,
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
