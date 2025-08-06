package actions

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payment"
	"github.com/flare-foundation/tee-node/pkg/types"

	xrpladdress "github.com/flare-foundation/go-flare-common/pkg/xrpl/address"
	xrputils "github.com/flare-foundation/go-flare-common/pkg/xrpl/signing/utils"

	"github.com/flare-foundation/go-flare-common/pkg/xrpl/signing/secp256k1"

	xrpltypes "github.com/flare-foundation/go-flare-common/pkg/xrpl/encoding/types"

	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/stretchr/testify/require"
)

type SignerData struct {
	Account       string
	SigningPubKey string
	TxnSignature  string
}

type TransactionData struct {
	Tx      map[string]any
	TxHash  []byte
	Signers []SignerData
}

func SignTransaction(t *testing.T, pc *utils.ProxyConfig, teeId common.Address, paymentInstruction payment.ITeePaymentsPaymentInstructionMessage, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32) TransactionData {
	originalMessageEncoded, err := abi.Arguments{payment.MessageArguments[constants.Pay]}.Pack(paymentInstruction)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, constants.XRP, constants.Pay, originalMessageEncoded, timestamp, nil, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionId, types.Threshold, constants.XRP, constants.Pay, pc.TeeId)

	var txData map[string]any
	err = json.Unmarshal(res.Result.Data, &txData)
	require.NoError(t, err)

	txData["SigningPubKey"] = ""

	encoded, err := xrpltypes.Encode(txData, true)
	require.NoError(t, err)

	signers := make([]SignerData, 0)
	SignersData, ok := txData["Signers"].([]any)
	require.True(t, ok)

	var txHash []byte
	for _, signerData := range SignersData {
		signerWrapper, ok := signerData.(map[string]any)
		require.True(t, ok)
		signerDataMap, ok := signerWrapper["Signer"].(map[string]any)
		require.True(t, ok)

		Account, ok := signerDataMap["Account"].(string)
		require.True(t, ok)
		SigningPubKey, ok := signerDataMap["SigningPubKey"].(string)
		require.True(t, ok)
		TxnSignature, ok := signerDataMap["TxnSignature"].(string)
		require.True(t, ok)

		accId, err := xrpladdress.ID(Account)
		require.NoError(t, err)

		txHash = xrputils.Prepare(encoded, true, accId)
		sigDER, err := hex.DecodeString(TxnSignature)
		require.NoError(t, err)

		ok, err = secp256k1.Validate(txHash, sigDER, SigningPubKey)
		require.NoError(t, err)
		require.True(t, ok)

		sd := SignerData{
			Account:       Account,
			SigningPubKey: SigningPubKey,
			TxnSignature:  TxnSignature,
		}
		signers = append(signers, sd)
	}

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionId, constants.XRP, constants.Pay, receipts)

	votingStatus := utils.GetVotingStatus(t, pc, rewardEpochId, iData.InstructionId)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, utils.TotalWeight/2)

	return TransactionData{
		Tx:      txData,
		TxHash:  txHash,
		Signers: signers,
	}
}
