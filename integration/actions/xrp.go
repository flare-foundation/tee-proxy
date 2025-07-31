package actions

import (
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payment"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/stretchr/testify/require"
)

func SignTransaction(t *testing.T, pc *utils.ProxyConfig, teeId common.Address, walletId [32]byte, keyId uint64, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32, paymentHash string) {
	originalMessage := payment.ITeePaymentsPaymentInstructionMessage{
		WalletId:         walletId,
		TeeIdKeyIdPairs:  []payment.TeeIdKeyIdPair{{TeeId: teeId, KeyId: keyId}},
		SenderAddress:    "rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G",
		RecipientAddress: "rJQesZZEQzW9J3Eb1X1Snc7E6YGk7kTMoK",
		Amount:           big.NewInt(1000000000),
		Fee:              big.NewInt(10),
		PaymentReference: [32]byte{},
		Nonce:            0,
		SubNonce:         0,
		BatchEndTs:       0,
	}

	originalMessageEncoded, err := abi.Arguments{payment.MessageArguments[constants.Pay]}.Pack(originalMessage)
	require.NoError(t, err)

	additionalFixedMessage := types.SignPaymentAdditionalFixedMessage{
		PaymentHash: paymentHash,
		KeyId:       keyId,
	}

	iData, err := utils.BuildInstructionData(constants.XRP, constants.Pay, originalMessageEncoded, additionalFixedMessage, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.GetActionResponse(t, pc.ExtPort, "result", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.Threshold, constants.XRP.Hash(), constants.Pay.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, teeId)
	require.NoError(t, err)

	var signatureData types.GetPaymentSignatureResponse
	err = json.Unmarshal(res.Result.Data, &signatureData)
	require.NoError(t, err)

	<-endOfVotingTicker.C
	res = utils.GetActionResponse(t, pc.ExtPort, "rewarding-data", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.End, constants.XRP.Hash(), constants.Pay.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, teeId)
	require.NoError(t, err)

	rewData := new(types.RewardingData)
	err = json.Unmarshal(res.Result.Data, &rewData)
	require.NoError(t, err)
	require.Equal(t, rewData.VoteSequence.VoteHash, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]))

	err = teeUtils.VerifySignature(rewData.VoteSequence.VoteHash[:], rewData.Signature, teeId)
	require.NoError(t, err)
}
