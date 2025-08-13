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
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/verification"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/stretchr/testify/require"
)

func GetTeeAttestation(t *testing.T, pc *utils.ProxyConfig, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32) {
	challenge, err := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil))
	require.NoError(t, err)

	originalMessage := verification.ITeeVerificationTeeAttestation{
		TeeMachine: verification.ITeeMachineRegistryTeeMachineWithAttestationData{
			TeeId:        pc.TeeId,
			InitialTeeId: common.Address{},
			Url:          "bla",
			CodeHash:     [32]byte{},
			Platform:     [32]byte{},
		},
		Challenge: common.BigToHash(challenge),
	}
	originalMessageEncoded, err := abi.Arguments{verification.MessageArguments[op.TEEAttestation]}.Pack(originalMessage)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, op.Reg, op.TEEAttestation, originalMessageEncoded, timestamp, nil, nil, pc.TeeId, rewardEpochId)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionID, types.Threshold, op.Reg, op.TEEAttestation, pc.TeeId)

	var teeInfoResponse types.TeeInfoResponse
	err = json.Unmarshal(res.Result.Data, &teeInfoResponse)
	require.NoError(t, err)

	teePubKey, err := types.ParsePubKey(teeInfoResponse.TeeInfo.PublicKey)
	require.NoError(t, err)

	receivedTeeId := crypto.PubkeyToAddress(*teePubKey)
	require.Equal(t, receivedTeeId, pc.TeeId)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.Reg, op.TEEAttestation, receipts)
}
