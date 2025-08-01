package utils

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/stretchr/testify/require"
)

func BuildInstructionData(opType constants.OPType, opCommand constants.OPCommand, originalMessage []byte,
	additionalFixedMessageRaw any, additionalVariableMessage any, teeId common.Address, rewardEpochId uint32) (*instruction.Data, error) {
	instructionId, err := GenerateRandomBytes(32)
	if err != nil {
		return nil, err
	}
	return BuildInstructionDataWithId(common.BytesToHash(instructionId), opType, opCommand, originalMessage, additionalFixedMessageRaw, additionalVariableMessage, teeId, rewardEpochId)
}

func BuildInstructionDataWithId(instructionId common.Hash, opType constants.OPType, opCommand constants.OPCommand, originalMessage []byte,
	additionalFixedMessageRaw any, additionalVariableMessage any, teeId common.Address, rewardEpochId uint32) (*instruction.Data, error) {
	var additionalFixedMessage []byte
	var err error
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
		InstructionId:          instructionId,
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

	switch additionalVariableMessage := additionalVariableMessage.(type) {
	case []byte:
		iData.AdditionalVariableMessage = additionalVariableMessage
	default:
		iData.AdditionalVariableMessage, err = json.Marshal(additionalVariableMessage)
		if err != nil {
			return nil, err
		}
	}

	return iData, err
}

// GetActionResponse Makes request to localhost:port/action/handle/actionId every TestTimeConfig.Interval ms until TestTimeConfig.Timeout
func GetActionResponse(t *testing.T, port uint, handle string, actionId common.Hash) *types.ActionResponse {
	t.Helper()

	url := fmt.Sprintf("http://localhost:%d/action/%s/%s", port, handle, strings.TrimPrefix(actionId.String(), "0x"))
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
		if time.Since(start) > TestTimeConfig.Timeout {
			return nil
		}
		time.Sleep(TestTimeConfig.Interval)
	}
}

func SetProxyUrlOnTee(t *testing.T, port uint, proxyUrl string) {
	t.Helper()

	request := types.ConfigureProxyUrlRequest{
		Url: proxyUrl,
	}

	body, err := json.Marshal(request)
	require.NoError(t, err)

	url := fmt.Sprintf("http://localhost:%d/configure", port)
	fmt.Printf("Setting proxy url on tee: %s\n", url)
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
	VerifyReceiptsForMultipleInstructions(t, receipts, []*instruction.Data{iData})
}

func VerifyReceiptsForMultipleInstructions(t *testing.T, receipts []*voting.SignedReceipt, instructions []*instruction.Data) {
	t.Helper()

	sort.Slice(receipts, func(i, j int) bool {
		return receipts[i].Receipt.Sequence < receipts[j].Receipt.Sequence
	})

	insHash, err := instructions[0].HashFixed()
	require.NoError(t, err)

	initHash, err := instructions[0].InitialVoteHash()
	require.NoError(t, err)

	currentHash := initHash
	for i, receipt := range receipts {
		require.LessOrEqual(t, receipt.Receipt.Timestamp, uint64(time.Now().Unix()))
		require.GreaterOrEqual(t, receipt.Receipt.Timestamp, uint64(time.Now().Unix()-1))

		require.Equal(t, receipt.Receipt.Sequence, uint64(i))
		require.Equal(t, receipt.Receipt.InstructionHash[:], insHash[:])

		var iData *instruction.Data
		if len(instructions) == 1 {
			iData = instructions[0]
		} else {
			iData = instructions[i]
		}

		nextHash, err := instruction.NextVoteHash(currentHash, uint64(i), receipt.Receipt.Signature, iData.AdditionalVariableMessage, receipt.Receipt.Timestamp)
		require.NoError(t, err)
		require.Equal(t, receipt.Receipt.VoteHash[:], nextHash[:])

		currentHash = nextHash
	}
}

func VerifyActionResponse(t *testing.T, res *types.ActionResponse, submissionTag types.SubmissionTag, opType, opCommand common.Hash) {
	require.True(t, res.Result.Status)
	require.Equal(t, submissionTag, res.Result.SubmissionTag)
	require.Equal(t, opType, res.Result.OPType)
	require.Equal(t, opCommand, res.Result.OPCommand)
}
