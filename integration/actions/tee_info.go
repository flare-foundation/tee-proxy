package actions

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/verification"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/stretchr/testify/require"
)

func GetTeeAttestation(t *testing.T, pc *utils.ProxyConfig, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32) {
	challenge, err := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil))
	require.NoError(t, err)

	originalMessage := verification.ITeeVerificationTeeAttestation{
		TeeMachine: verification.ITeeRegistryTeeMachineWithAttestationData{
			TeeId:        pc.TeeId,
			InitialTeeId: common.Address{},
			Url:          "bla",
			CodeHash:     [32]byte{},
			Platform:     [32]byte{},
		},
		Challenge: [32]byte(challenge.Bytes()),
	}
	originalMessageEncoded, err := abi.Arguments{verification.MessageArguments[constants.TEEAttestation]}.Pack(originalMessage)
	require.NoError(t, err)

	iData, err := utils.BuildInstructionData(constants.Reg, constants.TEEAttestation, originalMessageEncoded, nil, nil, pc.TeeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.GetActionResponse(t, pc.ExtPort, "result", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.Threshold, constants.Reg.Hash(), constants.TEEAttestation.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeId)
	require.NoError(t, err)

	var teeInfoResponse types.TeeInfoResponse
	err = json.Unmarshal(res.Result.Data, &teeInfoResponse)
	require.NoError(t, err)

	teePubKey, err := types.ParsePubKey(teeInfoResponse.TeeInfo.PublicKey)
	require.NoError(t, err)

	receivedTeeId := crypto.PubkeyToAddress(*teePubKey)
	require.Equal(t, receivedTeeId, pc.TeeId)

	<-endOfVotingTicker.C
	res = utils.GetActionResponse(t, pc.ExtPort, "rewarding-data", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.End, constants.Reg.Hash(), constants.TEEAttestation.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeId)
	require.NoError(t, err)

	signerSequence := new(types.RewardingData)
	err = json.Unmarshal(res.Result.Data, &signerSequence)
	require.NoError(t, err)
	require.Equal(t, signerSequence.VoteSequence.VoteHash, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]))
}
