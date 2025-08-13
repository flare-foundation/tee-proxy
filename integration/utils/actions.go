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

	"github.com/ethereum/go-ethereum/crypto"
	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/stretchr/testify/require"
)

func BuildInstructionData(
	t *testing.T,
	opType op.Type,
	opCommand op.Command,
	originalMessage []byte,
	timestamp uint64,
	additionalFixedMessageRaw any,
	additionalVariableMessage any,
	teeId common.Address,
	rewardEpochId uint32,
) *instruction.Data {
	instructionId, err := testutil.GenerateRandomBytes(32)
	require.NoError(t, err)
	return BuildInstructionDataWithId(t, common.BytesToHash(instructionId), opType, opCommand, originalMessage, timestamp, additionalFixedMessageRaw, additionalVariableMessage, teeId, rewardEpochId)
}

func BuildInstructionDataWithId(
	t *testing.T,
	instructionId common.Hash,
	opType op.Type,
	opCommand op.Command,
	originalMessage []byte,
	timestamp uint64,
	additionalFixedMessageRaw any,
	additionalVariableMessage any,
	teeId common.Address,
	rewardEpochId uint32,
) *instruction.Data {
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
		InstructionID:          instructionId,
		TeeID:                  teeId,
		RewardEpochID:          rewardEpochId,
		OPType:                 opType.Hash(),
		OPCommand:              opCommand.Hash(),
		OriginalMessage:        originalMessage,
		AdditionalFixedMessage: additionalFixedMessage,
		Timestamp:              timestamp,
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

func SetProxyUrlOnTee(t *testing.T, port uint, proxyUrl string) {
	t.Helper()

	request := types.ConfigureProxyUrlRequest{
		Url: proxyUrl,
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/configure", port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func SignAndSendInstruction(t *testing.T, iData *instruction.Data, pk *ecdsa.PrivateKey, port uint) *voting.SignedReceipt {
	t.Helper()
	return SignAndSendInstructions(t, iData, []*ecdsa.PrivateKey{pk}, port)[0]
}

func SignAndSendInstructions(t *testing.T, iData *instruction.Data, pkeys []*ecdsa.PrivateKey, port uint) []*voting.SignedReceipt {
	t.Helper()

	var responses []*voting.SignedReceipt
	for _, pk := range pkeys {
		response := signAndSendSingleInstruction(t, iData, pk, port)
		responses = append(responses, response)
	}

	return responses
}

func SignAndSendInstructionsWithAddVarMsgs(t *testing.T, iData *instruction.Data, additionalVariableMessage []hexutil.Bytes, pkeys []*ecdsa.PrivateKey, port uint) ([]*voting.SignedReceipt, []instruction.Data) {
	t.Helper()

	if len(additionalVariableMessage) > 0 {
		require.Equal(t, len(additionalVariableMessage), len(pkeys))
	}

	instructions := make([]instruction.Data, 0)
	var receipts []*voting.SignedReceipt
	for i, pk := range pkeys {
		iData := *iData

		iData.AdditionalVariableMessage = additionalVariableMessage[i]

		r := signAndSendSingleInstruction(t, &iData, pk, port)
		receipts = append(receipts, r)
		instructions = append(instructions, iData)
	}

	return receipts, instructions
}

func signAndSendSingleInstruction(t *testing.T, iData *instruction.Data, pk *ecdsa.PrivateKey, port uint) *voting.SignedReceipt {
	t.Helper()

	h, err := iData.HashForSigning()
	require.NoError(t, err)

	sig, err := instruction.SignInstructionHash(h, pk)
	require.NoError(t, err)

	inst := &instruction.Instruction{
		Data:      *iData,
		Signature: sig,
	}

	body, err := json.Marshal(inst)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/instruction", port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))

	require.NoError(t, err)
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
	VerifyReceiptsForMultipleInstructions(t, receipts, []instruction.Data{*iData})
}

func VerifyReceiptsForMultipleInstructions(t *testing.T, receipts []*voting.SignedReceipt, insts []instruction.Data) {
	t.Helper()

	sort.Slice(receipts, func(i, j int) bool {
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

		require.Equal(t, receipt.Receipt.Sequence, uint64(i))
		require.Equal(t, receipt.Receipt.InstructionHash[:], insHash[:])

		var iData *instruction.Data
		if len(insts) > 1 {
			iData = &insts[i]
		} else {
			iData = &insts[0]
		}

		nextHash, err := instruction.NextVoteHash(currentHash, uint64(i), receipt.Receipt.Signature, iData.AdditionalVariableMessage, receipt.Receipt.Timestamp)
		require.NoError(t, err)
		require.Equal(t, receipt.Receipt.VoteHash[:], nextHash[:])

		currentHash = nextHash
	}
}

// VerifyActionResponse Verifies the action response against expected values and checks the signature
func VerifyActionResponse(t *testing.T, res *types.ActionResponse, submissionTag types.SubmissionTag, opType op.Type, opCommand op.Command, teeId common.Address) {
	require.True(t, res.Result.Status)
	require.Equal(t, submissionTag, res.Result.SubmissionTag)
	require.Equal(t, opType.Hash(), res.Result.OPType)
	require.Equal(t, opCommand.Hash(), res.Result.OPCommand)

	err := teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, teeId)
	require.NoError(t, err)
}

// VerifyVotingStatus Verifies number of cosigners, cosigners threshold, finalized and weight of the VoteStatus
func VerifyVotingStatus(t *testing.T, votingStatus *voting.VoteStatus, nCosigners, cosignersThreshold, threshold uint16) {
	require.Equal(t, 1, len(votingStatus.Status))
	require.Equal(t, nCosigners, votingStatus.Status[0].Cosigners)

	require.Equal(t, cosignersThreshold, votingStatus.Status[0].CosignersThreshold)
	require.True(t, votingStatus.Status[0].Finalized)
	require.GreaterOrEqual(t, votingStatus.Status[0].Weight, threshold)
	require.Equal(t, threshold, votingStatus.Status[0].Threshold)
}

// FetchAndVerifyActionResponse Fetches ActionResponse and verifies the signature
func FetchAndVerifyActionResponse(t *testing.T, port uint, handle string, actionId common.Hash, submissionTag types.SubmissionTag, opType op.Type, opCommand op.Command, teeId common.Address) *types.ActionResponse {
	t.Helper()

	url := fmt.Sprintf("http://localhost:%d/action/%s/%s", port, handle, strings.TrimPrefix(actionId.String(), "0x"))
	var res types.ActionResponse
	makeRequests(t, url, &res)

	require.True(t, res.Result.Status)
	require.Equal(t, submissionTag, res.Result.SubmissionTag)
	require.Equal(t, opType.Hash(), res.Result.OPType)
	require.Equal(t, opCommand.Hash(), res.Result.OPCommand)

	err := teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, teeId)
	require.NoError(t, err)

	return &res
}

// FetchAndVerifyRewardingData Fetches rewarding data and verifies the action response and vote sequence
func FetchAndVerifyRewardingData(t *testing.T, pc *ProxyConfig, instructionID common.Hash, opType op.Type, opCommand op.Command, receipts []*voting.SignedReceipt) {
	res := FetchAndVerifyActionResponse(t, pc.ExtPort, "rewarding-data", instructionID, types.End, opType, opCommand, pc.TeeId)

	rewData := new(types.RewardingData)
	err := json.Unmarshal(res.Result.Data, &rewData)
	require.NoError(t, err)
	require.Equal(t, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]), rewData.VoteSequence.VoteHash)

	err = teeUtils.VerifySignature(rewData.VoteSequence.VoteHash[:], rewData.Signature, pc.TeeId)
	require.NoError(t, err)
}
