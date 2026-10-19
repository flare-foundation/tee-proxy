package testutil

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	"github.com/stretchr/testify/require"
)

// SafeOwnerKeys are fixed Safe owner keys for tests.
var SafeOwnerKeys = []string{
	"1111111111111111111111111111111111111111111111111111111111111111",
	"2222222222222222222222222222222222222222222222222222222222222222",
	"3333333333333333333333333333333333333333333333333333333333333333",
}

// SignSafeTx returns the owner-signature blob over the Safe transaction hash
// (Safe v ∈ {27, 28}) for the given owner keys.
func SignSafeTx(t *testing.T, txHash common.Hash, keys []string) []byte {
	t.Helper()

	blob := make([]byte, 0, len(keys)*safe.SignatureLength)
	for _, k := range keys {
		priv, err := crypto.HexToECDSA(k)
		require.NoError(t, err)
		raw, err := crypto.Sign(txHash[:], priv)
		require.NoError(t, err)
		raw[64] += 27
		blob = append(blob, raw...)
	}

	return blob
}

// OwnerAddresses returns the addresses of the given owner keys.
func OwnerAddresses(t *testing.T, keys []string) []common.Address {
	t.Helper()

	addrs := make([]common.Address, len(keys))
	for i, k := range keys {
		priv, err := crypto.HexToECDSA(k)
		require.NoError(t, err)
		addrs[i] = crypto.PubkeyToAddress(priv.PublicKey)
	}

	return addrs
}
