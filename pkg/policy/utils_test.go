package policy

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestSafeUint(t *testing.T) {
	tests32Edge := []uint32{0, 1, 0xffffffff}

	for _, test := range tests32Edge {
		bi := new(big.Int).SetUint64(uint64(test))

		result, err := SafeUint32(bi)
		require.NoError(t, err)
		require.Equal(t, test, result)
	}

	tests64Edge := []uint64{0, 1, 0xffffffffffffffff}

	for _, test := range tests64Edge {
		bi := new(big.Int).SetUint64(test)

		result, err := SafeUint64(bi)
		require.NoError(t, err)
		require.Equal(t, test, result)
	}

	for range 10 {
		x := rand.Uint32()
		bi := new(big.Int).SetUint64(uint64(x))
		resultX, err := SafeUint32(bi)
		require.NoError(t, err)

		require.Equal(t, x, resultX)

		y := rand.Uint64()
		bi = new(big.Int).SetUint64(y)
		resultY, err := SafeUint64(bi)
		require.NoError(t, err)

		require.Equal(t, y, resultY)
	}
}

func TestSafeUintFail(t *testing.T) {
	testFailBoth := []string{
		"-1",
		"0x010000000000000000",
		"0x010000000000000001",
	}

	for _, test := range testFailBoth {
		bi, ok := new(big.Int).SetString(test, 0)
		require.True(t, ok)

		_, err := SafeUint32(bi)
		require.Error(t, err)
		_, err = SafeUint64(bi)
		require.Error(t, err)
	}

	bi := new(big.Int).SetUint64(0x0100000000)
	_, err := SafeUint32(bi)
	require.Error(t, err)
}

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

func TestUint32ToHash(t *testing.T) {
	zero28 := make([]byte, 28)

	roundsRandom := 10

	for range roundsRandom {
		x := rand.Uint32()

		hash := Uint32ToHash(x)

		bi := new(big.Int).SetBytes(hash[:])

		y, err := SafeUint32(bi)
		require.NoError(t, err)

		require.Equal(t, zero28, hash[:28])
		require.Equal(t, x, y)
	}
}
