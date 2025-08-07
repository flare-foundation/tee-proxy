package testutil

import (
	"crypto/ecdsa"
	"io"
	"math/big"
	"math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/policy"

	cryptorand "crypto/rand"
)

var TestSigningPolicy *policy.SigningPolicy

var (
	PrivKey1 *ecdsa.PrivateKey
	PrivKey2 *ecdsa.PrivateKey
	PrivKey3 *ecdsa.PrivateKey
)

var MockSigningPolicy *policy.SigningPolicy // With 100 signers closer to production
var MockPrivKeys []*ecdsa.PrivateKey

const TotalWeight = 1<<16 - 1

func init() {
	var err error

	PrivKey1, err = crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}
	PrivKey2, err = crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}
	PrivKey3, err = crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}

	voters := make([]common.Address, 0, 3)

	voters = append(voters, crypto.PubkeyToAddress(PrivKey1.PublicKey))
	voters = append(voters, crypto.PubkeyToAddress(PrivKey2.PublicKey))
	voters = append(voters, crypto.PubkeyToAddress(PrivKey3.PublicKey))

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(1),
		StartVotingRoundId: 0,
		Threshold:          3,
		Seed:               big.NewInt(2),
		Voters:             voters,
		Weights:            []uint16{1, 3, 3},
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}

	TestSigningPolicy = policy.NewSigningPolicy(&event, nil)

	// * ================================================

	addresses := make([]common.Address, 0, 100)
	for i := 0; i < 100; i++ {
		privKey, err := crypto.GenerateKey()
		if err != nil {
			panic("cannot generate key")
		}
		MockPrivKeys = append(MockPrivKeys, privKey)
		addresses = append(addresses, crypto.PubkeyToAddress(privKey.PublicKey))
	}

	normalizedWeights := RandomNormalizedArray(len(addresses), 12345)
	weights := make([]uint16, len(addresses))
	weightSum := 0
	for i, w := range normalizedWeights[:99] {
		weights[i] = uint16(w * TotalWeight)
		weightSum += int(weights[i])
	}
	weights[99] = TotalWeight - uint16(weightSum)

	event = relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(1),
		StartVotingRoundId: 0,
		Threshold:          uint16(TotalWeight / 2),
		Seed:               big.NewInt(2),
		Voters:             addresses,
		Weights:            weights,
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}

	MockSigningPolicy = policy.NewSigningPolicy(&event, nil)
}

// RandomNormalizedArray generates an array of n random floats that sum to 1
func RandomNormalizedArray(n int, seed int64) []float64 {
	// Initialize random source with seed
	source := rand.NewSource(seed)
	r := rand.New(source)

	// Generate random numbers
	numbers := make([]float64, n)
	sum := 0.0

	for i := range n {
		// Generate random float between 0 and 1
		numbers[i] = r.Float64()
		sum += numbers[i]
	}

	// Normalize to sum to 1
	for i := range n {
		numbers[i] /= sum
	}

	return numbers
}

func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(cryptorand.Reader, b); err != nil {
		return nil, err
	}

	return b, nil
}
