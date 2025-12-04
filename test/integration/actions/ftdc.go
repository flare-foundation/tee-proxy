package actions

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/random"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/connector"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/verification"
	"github.com/flare-foundation/tee-node/pkg/ftdc"
	"github.com/flare-foundation/tee-node/pkg/types"

	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/test/integration/utils"
	"github.com/stretchr/testify/require"
)

const TotalWeight = 10000

func FTDCProve(
	t *testing.T,
	pc *utils.ProxyConfig,
	providerPrivKeys, cosignerPrivKeys []*ecdsa.PrivateKey,
	rewardEpochID uint32,
) *ftdc.ProveResponse {
	t.Helper()

	cosignerAddresses, cosignerAndProvider := CosignerAddressesAndProvider(cosignerPrivKeys, providerPrivKeys)
	cosignersThreshold := uint64(len(cosignerAddresses))
	originalMessage := connector.IFtdcHubFtdcAttestationRequest{
		Header: connector.IFtdcHubFtdcRequestHeader{
			AttestationType: [32]byte{},
			SourceId:        common.Hash{},
			ThresholdBIPS:   uint16(TotalWeight * 0.6),
		},
		RequestBody: make([]byte, 10),
	}

	originalMessageEncoded, err := ftdc.EncodeRequest(originalMessage)
	require.NoError(t, err)

	challenge, err := random.Hash()
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	additionalFixedMessageEncoded, variableMessages, privKeys, err := GetAdditionalFixedMessage(t, pc, challenge, originalMessage, timestamp, cosignerAndProvider, providerPrivKeys, cosignerPrivKeys, cosignerAddresses, cosignersThreshold)
	require.NoError(t, err)

	iData := utils.BuildInstructionData(t, op.FTDC, op.Prove, originalMessageEncoded, timestamp, additionalFixedMessageEncoded, nil, cosignerAddresses, cosignersThreshold, pc.TeeID, rewardEpochID)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts, instructions := utils.SignAndSendInstructionsWithAddVarMsgs(t, iData, variableMessages, privKeys, pc.ExtPort)

	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.FTDC, op.Prove, pc.TeeID)

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeID)
	require.NoError(t, err)

	var ftdcResponse ftdc.ProveResponse
	err = json.Unmarshal(res.Result.Data, &ftdcResponse)
	require.NoError(t, err)

	// Verify FTDC response signatures
	ftdcMsgHash, _, _, err := ftdc.HashMessage(originalMessage, additionalFixedMessageEncoded, cosignerAddresses, cosignersThreshold, timestamp)
	require.NoError(t, err)

	err = teeUtils.VerifySignature(ftdcMsgHash.Bytes(), ftdcResponse.TEESignature, pc.TeeID)
	require.NoError(t, err)

	require.Equal(t, len(ftdcResponse.CosignerSignatures), len(cosignerPrivKeys))
	for _, signature := range ftdcResponse.CosignerSignatures {
		_, err = teeUtils.CheckSignature(ftdcMsgHash.Bytes(), signature, cosignerAddresses)
		require.NoError(t, err)
	}

	require.Equal(t, ftdcResponse.ResponseBody, hexutil.Bytes(additionalFixedMessageEncoded))

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.FTDC, op.Prove, receipts)

	return &ftdcResponse
}

func CosignerAddressesAndProvider(cosignerPrivKeys []*ecdsa.PrivateKey, providerPrivKeys []*ecdsa.PrivateKey) ([]common.Address, map[common.Address]bool) {
	cosignerAddresses := make([]common.Address, len(cosignerPrivKeys))
	cosignerAndProvider := make(map[common.Address]bool)

	for j, cosignerPrivKey := range cosignerPrivKeys {
		cosignerAddresses[j] = crypto.PubkeyToAddress(cosignerPrivKey.PublicKey)
		for _, providerPrivKey := range providerPrivKeys {
			if cosignerAddresses[j] == crypto.PubkeyToAddress(providerPrivKey.PublicKey) {
				cosignerAndProvider[cosignerAddresses[j]] = true
			}
		}
	}
	return cosignerAddresses, cosignerAndProvider
}

// GetAdditionalFixedMessage returns the additional fixed message, the variable messages (signatures) and the private keys for the provider and cosigner
func GetAdditionalFixedMessage(t *testing.T, pc *utils.ProxyConfig, challenge [32]byte, originalMessage connector.IFtdcHubFtdcAttestationRequest, timestamp uint64, cosignerAndProvider map[common.Address]bool, providerPrivKeys []*ecdsa.PrivateKey, cosignerPrivKeys []*ecdsa.PrivateKey, cosignerAddresses []common.Address, cosignersThreshold uint64) ([]byte, []hexutil.Bytes, []*ecdsa.PrivateKey, error) {
	t.Helper()

	additionalFixedMessage := verification.ITeeVerificationTeeAttestation{
		TeeMachine: verification.ITeeMachineRegistryTeeMachineWithAttestationData{
			TeeId:        pc.TeeID,
			InitialTeeId: common.Address{},
			Url:          "blabla",
			CodeHash:     [32]byte{},
			Platform:     [32]byte{},
		},
		Challenge: challenge,
	}

	additionalFixedMessageEncoded, err := types.EncodeTeeAttestationRequest(&additionalFixedMessage)
	require.NoError(t, err)

	ftdcMsgHash, _, _, err := ftdc.HashMessage(originalMessage, additionalFixedMessageEncoded, cosignerAddresses, cosignersThreshold, timestamp)
	require.NoError(t, err)

	variableMessages := make([]hexutil.Bytes, 0)
	privKeys := make([]*ecdsa.PrivateKey, 0)
	for _, privKey := range providerPrivKeys {
		variableMessage, err := teeUtils.Sign(ftdcMsgHash[:], privKey)
		require.NoError(t, err)

		variableMessages = append(variableMessages, variableMessage)
		privKeys = append(privKeys, privKey)
	}
	for _, privKey := range cosignerPrivKeys {
		if _, check := cosignerAndProvider[crypto.PubkeyToAddress(privKey.PublicKey)]; check {
			continue
		}
		variableMessage, err := teeUtils.Sign(ftdcMsgHash[:], privKey)
		require.NoError(t, err)

		variableMessages = append(variableMessages, variableMessage)
		privKeys = append(privKeys, privKey)
	}

	return additionalFixedMessageEncoded, variableMessages, privKeys, nil
}
