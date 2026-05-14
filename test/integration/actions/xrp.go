package actions

import (
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payments"
	"github.com/flare-foundation/go-flare-common/pkg/tee/xrpl"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/test/integration/utils"
	"github.com/stretchr/testify/require"
)

func SignTransaction(
	t *testing.T,
	pc *utils.ProxyConfig,
	teeID common.Address,
	paymentInstruction payments.ITeePaymentsPaymentInstructionMessage,
	privKeys []*ecdsa.PrivateKey,
	rewardEpochID uint32,
) *types.ActionResponse {
	t.Helper()

	originalMessageEncoded, err := abi.Arguments{payments.MessageArguments[op.Pay]}.Pack(paymentInstruction)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, op.XRP, op.Pay, originalMessageEncoded, timestamp, nil, nil, nil, 0, teeID, rewardEpochID)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	feeSchedule, err := xrpl.ParseFeeEntries(paymentInstruction.FeeSchedule)
	require.NoError(t, err)
	startTime := time.Now()
	var res *types.ActionResponse
	if feeSchedule[0].Delay != 0 {
		res = utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.XRP, op.Pay, teeID, 2)
	}

	for i, entry := range feeSchedule {
		time.Sleep(time.Until(startTime.Add(entry.Delay + (500 * time.Millisecond))))

		expectedStatus := uint8(3 + i)
		if i == len(feeSchedule)-1 {
			expectedStatus = 1
		}
		res = utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.XRP, op.Pay, teeID, expectedStatus)
	}

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.XRP, op.Pay, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochID, iData.InstructionID)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, testutil.TotalWeight/2)

	return res
}
