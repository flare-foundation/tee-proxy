package queue

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestActionQueues(t *testing.T) {
	mr := miniredis.RunT(t)
	c := NewClient(mr.Addr())

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	q := NewActionQueues(c)

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

	err := q.Enqueue(ctx, action, Main)
	require.NoError(t, err)

	retrievedAction, err := q.Pop(ctx, Main)
	require.NoError(t, err)

	require.Equal(t, *action, *retrievedAction)
}
