package policy

import (
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestAddressToHash(t *testing.T) {
	zero12 := make([]byte, 12)

	roundsRandom := 10

	for range roundsRandom {
		x, err := crypto.GenerateKey()
		require.NoError(t, err)

		addr := crypto.PubkeyToAddress(x.PublicKey)

		hash := AddressToHash(addr)

		require.Equal(t, zero12, hash[:12])
		require.Equal(t, addr[:], hash[12:])
	}
}
