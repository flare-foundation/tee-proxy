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
	"github.com/flare-foundation/tee-proxy/test/integration/utils"
	"github.com/stretchr/testify/require"
)

func GetTeeAttestation(t *testing.T, pc *utils.ProxyConfig, privKeys []*ecdsa.PrivateKey, rewardEpochID uint32) {
	t.Helper()

	challenge, err := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil))
	require.NoError(t, err)

	originalMessage := verification.IVerificationTeeAttestation{
		TeeMachine: verification.IMachineManagerTeeMachineWithAttestationData{
			TeeId:        pc.TeeID,
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
	iData := utils.BuildInstructionData(t, op.Reg, op.TEEAttestation, originalMessageEncoded, timestamp, nil, nil, nil, 0, pc.TeeID, rewardEpochID)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.Reg, op.TEEAttestation, pc.TeeID, 1)

	var teeInfoResponse types.TeeInfoResponse
	err = json.Unmarshal(res.Result.Data, &teeInfoResponse)
	require.NoError(t, err)

	teePubKey, err := types.ParsePubKey(teeInfoResponse.TeeInfo.PublicKey)
	require.NoError(t, err)

	receivedTeeId := crypto.PubkeyToAddress(*teePubKey)
	require.Equal(t, receivedTeeId, pc.TeeID)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.Reg, op.TEEAttestation, receipts)
}
