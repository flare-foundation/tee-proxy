package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"github.com/stretchr/testify/require"
)

func TestActionQueues(t *testing.T) {
	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	q := NewActionQueues(c, time.Hour)

	ctx := context.Background()

	action := &types.Action{
		Data: types.ActionData{
			ID:            crypto.Keccak256Hash([]byte("id")),
			Type:          types.Direct,
			SubmissionTag: types.Threshold,
			Message:       hexutil.Bytes{},
		},
		AdditionalVariableMessages: []hexutil.Bytes{},
		Timestamps:                 []uint64{},
		AdditionalActionData:       hexutil.Bytes{},
		Signatures:                 []hexutil.Bytes{},
	}

	err := q.Enqueue(ctx, action, processorutils.Main)
	require.NoError(t, err)

	retrievedAction, err := q.Dequeue(ctx, processorutils.Main)
	require.NoError(t, err)

	require.Equal(t, *action, *retrievedAction)
}
