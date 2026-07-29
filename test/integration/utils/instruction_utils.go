package utils

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/random"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/voting"
	"github.com/stretchr/testify/require"
)

// TestChainID is the chain ID embedded in every integration instruction and configured on the
// TEE via SetChainIDOnTEE. The instruction's signed hash and the TEE's expectedChainID must agree.
const TestChainID uint64 = 14

// ActionResultSignHash recomputes the domain-separated preimage the TEE signs over an action
// result: signing.Payload{TEEActionResult, TestChainID, ActionResult.Hash()}.Hash(). The returned
// bytes are suitable for teeUtils.VerifySignature, which applies the EIP-191 text-hash prefix.
func ActionResultSignHash(t *testing.T, innerHash []byte) []byte {
	t.Helper()

	signHash, err := csigning.NewPayload(csigning.TEEActionResult, TestChainID, common.BytesToHash(innerHash)).Hash()
	require.NoError(t, err)

	return signHash[:]
}

func BuildInstructionData(
	t *testing.T,
	opType op.Type,
	opCommand op.Command,
	originalMessage []byte,
	timestamp uint64,
	additionalFixedMessageRaw any,
	additionalVariableMessage any,
	cosigners []common.Address,
	cosignersThreshold uint64,
	teeID common.Address,
	rewardEpochID uint32,
) *instruction.Data {
	t.Helper()
	instructionID, err := random.Hash()
	require.NoError(t, err)
	return BuildInstructionDataWithID(t, instructionID, opType, opCommand, originalMessage, timestamp, additionalFixedMessageRaw, additionalVariableMessage, cosigners, cosignersThreshold, teeID, rewardEpochID)
}

func BuildInstructionDataWithID(
	t *testing.T,
	instructionID common.Hash,
	opType op.Type,
	opCommand op.Command,
	originalMessage []byte,
	timestamp uint64,
	additionalFixedMessageRaw any,
	additionalVariableMessage any,
	cosigners []common.Address,
	cosignersThreshold uint64,
	teeID common.Address,
	rewardEpochID uint32,
) *instruction.Data {
	t.Helper()

	var additionalFixedMessage []byte
	var err error
	switch additionalFixedMessageRaw := additionalFixedMessageRaw.(type) {
	case nil:
		additionalFixedMessage = []byte{}
	case []byte:
		additionalFixedMessage = additionalFixedMessageRaw
	default:
		additionalFixedMessage, err = json.Marshal(additionalFixedMessageRaw)
	}
	require.NoError(t, err)

	instructionDataFixed := instruction.DataFixed{
		InstructionID:          instructionID,
		TeeID:                  teeID,
		RewardEpochID:          rewardEpochID,
		OPType:                 opType.Hash(),
		OPCommand:              opCommand.Hash(),
		OriginalMessage:        originalMessage,
		AdditionalFixedMessage: additionalFixedMessage,
		Timestamp:              timestamp,
		Cosigners:              cosigners,
		CosignersThreshold:     cosignersThreshold,
	}

	iData := &instruction.Data{
		DataFixed:                 instructionDataFixed,
		AdditionalVariableMessage: []byte(""),
	}

	switch additionalVariableMessage := additionalVariableMessage.(type) {
	case nil:
		iData.AdditionalVariableMessage = []byte{}
	case []byte:
		iData.AdditionalVariableMessage = additionalVariableMessage
	default:
		iData.AdditionalVariableMessage, err = json.Marshal(additionalVariableMessage)
		require.NoError(t, err)
	}

	return iData
}

func SignAndSendInstruction(t *testing.T, iData *instruction.Data, privKey *ecdsa.PrivateKey, port uint) *voting.SignedReceipt {
	t.Helper()
	return SignAndSendInstructions(t, iData, []*ecdsa.PrivateKey{privKey}, port)[0]
}

func SignAndSendInstructions(t *testing.T, iData *instruction.Data, privKeys []*ecdsa.PrivateKey, port uint) []*voting.SignedReceipt {
	t.Helper()

	responses := make([]*voting.SignedReceipt, 0, len(privKeys))
	for _, key := range privKeys {
		response := signAndSendSingleInstruction(t, iData, key, port)
		responses = append(responses, response)
	}

	return responses
}

func SignAndSendInstructionsWithAddVarMsgs(t *testing.T, iData *instruction.Data, additionalVariableMessage []hexutil.Bytes, privKeys []*ecdsa.PrivateKey, port uint) ([]*voting.SignedReceipt, []instruction.Data) {
	t.Helper()

	if len(additionalVariableMessage) > 0 {
		require.Equal(t, len(additionalVariableMessage), len(privKeys))
	}

	instructions := make([]instruction.Data, 0, len(privKeys))
	receipts := make([]*voting.SignedReceipt, 0, len(privKeys))
	for i, key := range privKeys {
		iData := *iData

		iData.AdditionalVariableMessage = additionalVariableMessage[i]

		r := signAndSendSingleInstruction(t, &iData, key, port)
		receipts = append(receipts, r)
		instructions = append(instructions, iData)
	}

	return receipts, instructions
}

func signAndSendSingleInstruction(t *testing.T, iData *instruction.Data, priv *ecdsa.PrivateKey, port uint) *voting.SignedReceipt {
	t.Helper()

	h, err := iData.HashForSigning(TestChainID)
	require.NoError(t, err)

	sig, err := instruction.SignInstructionHash(h, priv)
	require.NoError(t, err)

	inst := &instruction.Instruction{
		Data:      *iData,
		Signature: sig,
	}

	body, err := json.Marshal(inst)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/instruction", port)

	// A policy pushed to the proxy is applied asynchronously: ListenToPolicies creates the
	// voting round in a separate goroutine, so an instruction submitted right after a policy
	// change can outrun the round and get a 404 (the only 404 from POST /instruction is
	// "no round"). Retry on 404 within the test timeout; any other status is returned as-is.
	var resp *http.Response
	deadline := time.Now().Add(TestTimeConfig.Timeout)
	for {
		resp, err = http.Post(url, "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		if resp.StatusCode != http.StatusNotFound || time.Now().After(deadline) {
			break
		}
		resp.Body.Close() //nolint:errcheck
		time.Sleep(TestTimeConfig.Interval)
	}

	defer resp.Body.Close() //nolint:errcheck

	require.Equalf(t, http.StatusOK, resp.StatusCode, "data for %s, %s", op.HashToOPType(iData.OPType), op.HashToOPCommand(iData.OPCommand))

	var res voting.SignedReceipt
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	err = dec.Decode(&res)
	require.NoError(t, err)

	return &res
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
		require.Equal(t, receipt.Receipt.InstructionHash, insHash)

		nextHash, err := instruction.NextVoteHash(currentHash, uint64(i), receipt.Receipt.Signature, iData.AdditionalVariableMessage, receipt.Receipt.Timestamp)
		require.NoError(t, err)
		require.Equal(t, receipt.Receipt.VoteHash, nextHash)

		currentHash = nextHash
	}
}

func VerifyReceiptsForMultipleInstructions(t *testing.T, receipts []*voting.SignedReceipt, insts []instruction.Data) {
	t.Helper()

	require.Equal(t, len(receipts), len(insts))

	if len(receipts) == 0 {
		return
	}

	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i].Receipt.Sequence < receipts[j].Receipt.Sequence
	})
	sort.Slice(insts, func(i, j int) bool {
		return receipts[i].Receipt.Sequence < receipts[j].Receipt.Sequence
	})

	insHash, err := insts[0].HashFixed()
	require.NoError(t, err)

	initHash, err := insts[0].InitialVoteHash()
	require.NoError(t, err)

	currentHash := initHash
	for i, receipt := range receipts {
		require.LessOrEqual(t, receipt.Receipt.Timestamp, uint64(time.Now().Unix()))
		require.GreaterOrEqual(t, receipt.Receipt.Timestamp, uint64(time.Now().Unix()-1))

		require.Equal(t, receipt.Receipt.Sequence, uint64(i), "invalid sequence")
		require.Equal(t, receipt.Receipt.InstructionHash, insHash)

		insHashCheck, err := insts[i].HashFixed()
		require.NoError(t, err)

		require.Equal(t, insHash, insHashCheck)

		nextHash, err := instruction.NextVoteHash(currentHash, uint64(i), receipt.Receipt.Signature, insts[i].AdditionalVariableMessage, receipt.Receipt.Timestamp)
		require.NoError(t, err)
		require.Equal(t, receipt.Receipt.VoteHash, nextHash)

		currentHash = nextHash
	}
}

// VerifyActionResponse Verifies the action response against expected values and checks the signature
func VerifyActionResponse(t *testing.T, res *types.ActionResponse, submissionTag types.SubmissionTag, opType op.Type, opCommand op.Command, teeID common.Address) {
	t.Helper()

	require.Equal(t, uint8(1), res.Result.Status)
	require.Equal(t, submissionTag, res.Result.SubmissionTag)
	require.Equal(t, opType.Hash(), res.Result.OPType)
	require.Equal(t, opCommand.Hash(), res.Result.OPCommand)

	err := teeUtils.VerifySignature(ActionResultSignHash(t, res.Result.Hash()), res.Signature, teeID)
	require.NoError(t, err)
}

// VerifyVotingStatus Verifies number of cosigners, cosigners threshold, finalized and weight of the VoteStatus
func VerifyVotingStatus(t *testing.T, statuses *voting.Statuses, nCosigners, cosignersThreshold, threshold uint16) {
	t.Helper()

	require.Equal(t, 1, len(statuses.Status))
	require.Equal(t, nCosigners, statuses.Status[0].Cosigners)

	require.Equal(t, cosignersThreshold, statuses.Status[0].CosignersThreshold)
	require.True(t, statuses.Status[0].Finalized)
	require.GreaterOrEqual(t, statuses.Status[0].Weight, threshold)
	require.Equal(t, threshold, statuses.Status[0].Threshold)
}

// FetchAndVerifyActionResponse Fetches ActionResponse and verifies the signature
func FetchAndVerifyActionResponse(t *testing.T, port uint, actionID common.Hash, submissionTag types.SubmissionTag, opType op.Type, opCommand op.Command, teeID common.Address, expectedStatus uint8) *types.ActionResponse {
	t.Helper()

	url := fmt.Sprintf("http://localhost:%d/action/result/%s?submissionTag=%s", port, strings.TrimPrefix(actionID.String(), "0x"), submissionTag)
	var res types.ActionResponse
	makeRequests(t, url, &res)

	require.Equal(t, expectedStatus, res.Result.Status)
	require.Equal(t, submissionTag, res.Result.SubmissionTag)
	require.Equal(t, opType.Hash(), res.Result.OPType)
	require.Equal(t, opCommand.Hash(), res.Result.OPCommand)

	err := teeUtils.VerifySignature(ActionResultSignHash(t, res.Result.Hash()), res.Signature, teeID)
	require.NoError(t, err)

	return &res
}

// FetchAndVerifyRewardingData Fetches rewarding data and verifies the action response and vote sequence
func FetchAndVerifyRewardingData(t *testing.T, pc *ProxyConfig, instructionID common.Hash, opType op.Type, opCommand op.Command, receipts []*voting.SignedReceipt) {
	t.Helper()

	res := FetchAndVerifyActionResponse(t, pc.ExtPort, instructionID, types.End, opType, opCommand, pc.TeeID, 1)

	rewData := new(types.RewardingData)
	err := json.Unmarshal(res.Result.Data, &rewData)
	require.NoError(t, err)
	require.Greater(t, len(receipts), 0)
	require.Equal(t, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]), rewData.VoteSequence.VoteHash)

	voteSignHash, err := csigning.NewPayload(csigning.TEEVoteHash, TestChainID, rewData.VoteSequence.VoteHash).Hash()
	require.NoError(t, err)
	err = teeUtils.VerifySignature(voteSignHash[:], rewData.Signature, pc.TeeID)
	require.NoError(t, err)
}
