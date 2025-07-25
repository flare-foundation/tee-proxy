package integration

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/stretchr/testify/require"

	cryptorand "crypto/rand"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
)

func EncodeSigningPolicy(policy *relay.RelaySigningPolicyInitialized) ([]byte, error) {
	// Validation
	if policy == nil {
		return nil, fmt.Errorf("signing policy is undefined")
	}

	voters := policy.Voters
	if len(voters) > 65535 { // 2^16 - 1
		return nil, fmt.Errorf("too many signers")
	}
	if len(policy.Weights) != len(voters) {
		return nil, fmt.Errorf("number of voters and weights do not match")
	}

	// Validate reward epoch ID
	if policy.RewardEpochId.Int64() > 16777215 { // 2^24 - 1
		return nil, fmt.Errorf("reward epoch id out of range: %d", policy.RewardEpochId.Int64())
	}

	// Validate seed
	seedBytes := policy.Seed.Bytes()
	if len(seedBytes) > 32 {
		return nil, fmt.Errorf("seed value too large")
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
	for i := 0; i < len(voters); i++ {
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

	for i := 0; i < numVoters; i++ {
		voterPrivKey, err := utils.GenerateEthereumPrivateKey()
		if err != nil {
			panic(err)
		}
		voterPubKey := voterPrivKey.PublicKey

		privKeys[i] = voterPrivKey
		voters[i] = utils.PubkeyToAddress(&voterPubKey)
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

const TotalWeight = 1<<16 - 1

func GenerateRandomPolicyData(rewardEpochId uint32, voters []common.Address, seed int64) *policy.SigningPolicy {
	rgen := rand.New(rand.NewSource(seed))

	startVotingRoundId := rgen.Uint32()

	threshold := uint16(TotalWeight / 2)
	randSeed := big.NewInt(rgen.Int63())
	weights := []uint16{}

	normalizedWeights := RandomNormalizedArray(len(voters), seed)
	for _, w := range normalizedWeights {
		weights = append(weights, uint16(w*TotalWeight))
	}

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(int64(rewardEpochId)),
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
	return policy.NewSigningPolicy(&event, nil)
}

// RandomNormalizedArray generates an array of n random floats that sum to 1
func RandomNormalizedArray(n int, seed int64) []float64 {
	// Initialize random source with seed
	source := rand.NewSource(seed)
	r := rand.New(source)

	// Generate random numbers
	numbers := make([]float64, n)
	sum := 0.0

	for i := 0; i < n; i++ {
		// Generate random float between 0 and 1
		numbers[i] = r.Float64()
		sum += numbers[i]
	}

	// Normalize to sum to 1
	for i := 0; i < n; i++ {
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

func BuildInstructionData(opType constants.OPType, opCommand constants.OPCommand, originalMessage []byte, additionalFixedMessageRaw interface{}, teeId common.Address, rewardEpochId uint32) (*instruction.Data, error) {
	instructionId, err := GenerateRandomBytes(32)
	if err != nil {
		return nil, err
	}

	var additionalFixedMessage []byte
	switch additionalFixedMessageRaw := additionalFixedMessageRaw.(type) {
	case []byte:
		additionalFixedMessage = additionalFixedMessageRaw
	default:
		additionalFixedMessage, err = json.Marshal(additionalFixedMessageRaw)
		if err != nil {
			return nil, err
		}
	}

	instructionDataFixed := instruction.DataFixed{
		InstructionId:          common.HexToHash(hex.EncodeToString(instructionId)),
		TeeId:                  teeId,
		RewardEpochId:          rewardEpochId,
		OpType:                 opType.Hash(),
		OpCommand:              opCommand.Hash(),
		OriginalMessage:        originalMessage,
		AdditionalFixedMessage: additionalFixedMessage,
	}

	iData := &instruction.Data{
		DataFixed:                 instructionDataFixed,
		AdditionalVariableMessage: []byte(""),
	}

	return iData, err
}

func GetActionResponse(t *testing.T, port uint, actionId common.Hash, timeout, interval time.Duration) *types.ActionResponse {
	t.Helper()

	url := fmt.Sprintf("http://localhost:%d/action/result/%s", port, strings.TrimPrefix(actionId.String(), "0x"))
	start := time.Now()
	for {
		resp, err := http.Get(url)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				bodyBytes, err := io.ReadAll(resp.Body)
				require.NoError(t, err)

				var res types.ActionResponse
				err = json.Unmarshal(bodyBytes, &res)
				require.NoError(t, err)

				return &res
			}
		}
		if time.Since(start) > timeout {
			require.FailNow(t, "Timeout waiting for action result")
			return nil
		}
		time.Sleep(interval)
	}
}

func SignAndSendInstructions(t *testing.T, iData *instruction.Data, pkeys []*ecdsa.PrivateKey, port uint) []*voting.SignedReceipt {
	t.Helper()

	var responses []*voting.SignedReceipt
	for _, pk := range pkeys {
		h, err := iData.HashForSigning()
		require.NoError(t, err)

		sig, err := instruction.SignInstructionHash(h, pk)
		require.NoError(t, err)

		i := &instruction.Instruction{
			Data:      *iData,
			Signature: sig,
		}

		body, err := json.Marshal(i)
		require.NoError(t, err)

		url := fmt.Sprintf("http://localhost:%d/instruction", port)
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var res voting.SignedReceipt
		dec := json.NewDecoder(resp.Body)
		dec.DisallowUnknownFields()
		err = dec.Decode(&res)
		require.NoError(t, err)
		responses = append(responses, &res)
	}

	return responses
}

func VerifyReceipts(t *testing.T, receipts []*voting.SignedReceipt, iData *instruction.Data) {
	t.Helper()

	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i].Receipt.Sequence < receipts[j].Receipt.Sequence
	})

	insHash, err := iData.HashFixed()
	require.NoError(t, err)

	initHash, err := iData.InitialVoteHash()
	require.NoError(t, err)

	currentHash := initHash
	for i, receipt := range receipts {
		require.LessOrEqual(t, receipt.Receipt.Timestamp, uint64(time.Now().Unix()))
		require.GreaterOrEqual(t, receipt.Receipt.Timestamp, uint64(time.Now().Unix()-1))

		require.Equal(t, receipt.Receipt.Sequence, uint64(i))
		require.Equal(t, receipt.Receipt.InstructionHash[:], insHash[:])

		nextHash, err := instruction.NextVoteHash(currentHash, uint64(i), receipt.Receipt.Signature, iData.AdditionalVariableMessage, receipt.Receipt.Timestamp)
		require.NoError(t, err)
		require.Equal(t, receipt.Receipt.VoteHash[:], nextHash[:])

		currentHash = nextHash
	}
}
