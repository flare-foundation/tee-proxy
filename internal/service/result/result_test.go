package result

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
)

// TestLastStorageErrSetAndClear covers the auto-recovery contract: the most recent
// storage outcome wins, so a successful store clears the prior error and liveness
// recovers without a pod restart.
func TestLastStorageErrSetAndClear(t *testing.T) {
	s := NewService(nil, uint64(14))

	require.NoError(t, s.LastStorageErr())

	boom := errors.New("redis down")
	s.recordStorageResult(boom)
	require.ErrorIs(t, s.LastStorageErr(), boom)

	s.recordStorageResult(nil)
	require.NoError(t, s.LastStorageErr())
}

// TestRecoverSignerChainIDBinding verifies that the TEE action-result signer is recovered
// against the chain-bound TEE_ACTION_RESULT preimage: a signature produced under one chain ID
// recovers to the signer under that chain ID and to a different address under another.
func TestRecoverSignerChainIDBinding(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	teeAddr := crypto.PubkeyToAddress(key.PublicKey)

	const signChainID = uint64(14)

	ar := &types.ActionResponse{
		Result: types.ActionResult{
			ID:            common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa"),
			SubmissionTag: types.Threshold,
			Status:        1,
			Data:          []byte(`{"ok":true}`),
		},
	}

	signHash, err := csigning.NewPayload(csigning.TEEActionResult, signChainID, common.BytesToHash(ar.Result.Hash())).Hash()
	require.NoError(t, err)
	sig, err := crypto.Sign(accounts.TextHash(signHash[:]), key)
	require.NoError(t, err)
	ar.Signature = sig

	got, err := recoverSigner(ar, signChainID)
	require.NoError(t, err)
	require.Equal(t, teeAddr, got,
		"signer must recover under the chain ID the signature was produced with")

	other, err := recoverSigner(ar, signChainID+1)
	require.NoError(t, err)
	require.NotEqual(t, teeAddr, other,
		"a result signed under one chain ID must not recover to the signer under another")
}

// TestProcessAndStoreDropsZeroIDResult verifies that a delivery-failure
// notification from the node (zero action ID) is dropped without error and
// without reaching storage, so it cannot collide on the zero key or surface as
// a spurious failed-result error during a proxy restart. The nil ResultStorage
// would nil-panic if the guard let execution fall through to a store.
func TestProcessAndStoreDropsZeroIDResult(t *testing.T) {
	s := NewService(nil, uint64(14))

	r := &types.ActionResponse{
		Result: types.ActionResult{
			ID:     common.Hash{},
			Status: 0,
			Log:    "error posting result: unexpected status code: 503",
		},
	}

	require.NoError(t, s.ProcessAndStore(context.Background(), r))
}
