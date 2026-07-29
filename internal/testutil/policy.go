package testutil

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
)

var TestSigningPolicy *policy.SigningPolicy

var (
	PrivKey1 *ecdsa.PrivateKey // has weight 1/7
	PrivKey2 *ecdsa.PrivateKey // has weight 3/7
	PrivKey3 *ecdsa.PrivateKey // has weight 3/7
)

var MockSigningPolicy, MockPrivKeys = GeneratePolicy(100, true)

const TotalWeight = 1<<16 - 1 // max uint16

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

	TestSigningPolicy, err = policy.NewSigningPolicy(&event, nil)
	if err != nil {
		panic(fmt.Sprintf("generating test policy: %v", err))
	}
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

// GeneratePolicy creates a policy with n providers and returns the policy and providers private keys.
//
// If randomWeights is true, the weights are randomized, if not, the weights are uniform.
func GeneratePolicy(n int, randomWeights bool) (*policy.SigningPolicy, []*ecdsa.PrivateKey) {
	privKeys := make([]*ecdsa.PrivateKey, n)
	addresses := make([]common.Address, n)
	for j := range n {
		privKey, err := crypto.GenerateKey()
		if err != nil {
			panic("cannot generate key")
		}
		privKeys[j] = privKey
		addresses[j] = crypto.PubkeyToAddress(privKey.PublicKey)
	}

	weights := make([]uint16, n)
	sum := uint16(0)

	if randomWeights {
		tempWeights := RandomNormalizedArray(n, 10)

		for j := range n {
			weights[j] = uint16(tempWeights[j] * TotalWeight)
			sum += weights[j]
		}
	} else {
		for j := range n {
			weights[j] = uint16(TotalWeight / n)
			sum += weights[j]
		}
	}

	th := sum / 2
	if sum%2 == 1 {
		th++
	}

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(1),
		StartVotingRoundId: 0,
		Threshold:          th,
		Seed:               big.NewInt(2),
		Voters:             addresses,
		Weights:            weights,
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}

	sp, err := policy.NewSigningPolicy(&event, nil)
	if err != nil {
		panic(fmt.Sprintf("generating policy: %v", err))
	}

	return sp, privKeys
}
