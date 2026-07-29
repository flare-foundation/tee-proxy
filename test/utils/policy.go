package utils

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/rand"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/policy"

	utils "github.com/flare-foundation/tee-node/pkg/utils"

	"github.com/flare-foundation/tee-proxy/internal/testutil"
)

func EncodeSigningPolicy(policy *relay.RelaySigningPolicyInitialized) ([]byte, error) {
	// Validation
	if policy == nil {
		return nil, errors.New("signing policy is undefined")
	}

	voters := policy.Voters
	if len(voters) > 65535 { // 2^16 - 1
		return nil, errors.New("too many signers")
	}
	if len(policy.Weights) != len(voters) {
		return nil, errors.New("number of voters and weights do not match")
	}

	// Validate reward epoch ID
	if policy.RewardEpochId.Int64() > 16777215 { // 2^24 - 1
		return nil, fmt.Errorf("reward epoch id out of range: %d", policy.RewardEpochId.Int64())
	}

	// Validate seed
	seedBytes := policy.Seed.Bytes()
	if len(seedBytes) > 32 {
		return nil, errors.New("seed value too large")
	}

	// Calculate total size
	// 2(numVoters) + 3(rewardEpoch) + 4(startVoting) + 2(threshold) + 32(seed) + len(voters)*(20+2)
	totalSize := 43 + len(voters)*22

	// Create result buffer
	result := make([]byte, totalSize)
	pos := 0

	// Write number of voters (2 bytes)
	binary.BigEndian.PutUint16(result[pos:], uint16(len(voters)))
	pos += 2

	// Write reward epoch ID (3 bytes)
	result[pos] = byte(policy.RewardEpochId.Int64() >> 16)
	result[pos+1] = byte(policy.RewardEpochId.Int64() >> 8)
	result[pos+2] = byte(policy.RewardEpochId.Int64())
	pos += 3

	// Write start voting round ID (4 bytes)
	binary.BigEndian.PutUint32(result[pos:], policy.StartVotingRoundId)
	pos += 4

	// Write threshold (2 bytes)
	binary.BigEndian.PutUint16(result[pos:], policy.Threshold)
	pos += 2

	// Write seed (32 bytes, pad if necessary)
	copy(result[pos+32-len(seedBytes):pos+32], seedBytes)
	pos += 32

	// Write voters and weights
	for i := range voters {
		// Write voter address (20 bytes)
		copy(result[pos:], voters[i][:])
		pos += 20

		// Write weight (2 bytes)
		binary.BigEndian.PutUint16(result[pos:], policy.Weights[i])
		pos += 2
	}

	return result, nil
}

func GenerateRandomKeys(numVoters int) ([]common.Address, []*ecdsa.PrivateKey, map[common.Address]*ecdsa.PublicKey) {
	voters := make([]common.Address, numVoters)
	privKeys := make([]*ecdsa.PrivateKey, numVoters)
	pubKeys := make(map[common.Address]*ecdsa.PublicKey)

	for i := range numVoters {
		voterPrivKey, err := crypto.GenerateKey()
		if err != nil {
			panic(err)
		}
		voterPubKey := voterPrivKey.PublicKey

		privKeys[i] = voterPrivKey
		voters[i] = crypto.PubkeyToAddress(voterPubKey)
		pubKeys[voters[i]] = &voterPubKey
	}

	return voters, privKeys, pubKeys
}

func GetVoterWeights(policy *policy.SigningPolicy) []uint16 {
	weights := make([]uint16, len(policy.Voters.Voters()))
	for i := range policy.Voters.Voters() {
		weights[i] = policy.Voters.VoterWeight(i)
	}

	return weights
}

func GenerateRandomPolicyData(rewardEpochID uint32, voters []common.Address, seed int64) *policy.SigningPolicy {
	rgen := rand.New(rand.NewSource(seed))

	startVotingRoundId := rgen.Uint32()

	threshold := uint16(testutil.TotalWeight / 2)
	randSeed := big.NewInt(rgen.Int63())
	normalizedWeights := testutil.RandomNormalizedArray(len(voters), seed)
	weights := make([]uint16, 0, len(normalizedWeights))
	for _, w := range normalizedWeights {
		weights = append(weights, uint16(w*testutil.TotalWeight))
	}

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(int64(rewardEpochID)),
		StartVotingRoundId: startVotingRoundId,
		Threshold:          threshold,
		Seed:               randSeed,
		Voters:             voters,
		Weights:            weights,
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}
	policyBytes, err := EncodeSigningPolicy(&event)
	if err != nil {
		panic(err)
	}
	event.SigningPolicyBytes = policyBytes

	p, err := policy.NewSigningPolicy(&event, nil)
	if err != nil {
		panic(fmt.Sprintf("generating random policy data: %v", err))
	}

	return p
}

func BuildMultiSignedPolicy(policyBytes []byte, voterPrivKeys []*ecdsa.PrivateKey) types.MultiSignedPolicy {
	sigs := make([][]byte, 0, len(voterPrivKeys))

	hash := policy.Hash(policyBytes)
	for _, voterPrivKey := range voterPrivKeys {
		sig, err := utils.Sign(hash, voterPrivKey)
		if err != nil {
			panic(err)
		}
		sigs = append(sigs, sig)
	}

	return types.MultiSignedPolicy{
		PolicyBytes: policyBytes,
		Signatures:  sigs,
	}
}
