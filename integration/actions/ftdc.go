package actions

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/connector"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/verification"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/stretchr/testify/require"
)

const TotalWeight = 10000

func FtdcProve(
	t *testing.T,
	pc *utils.ProxyConfig,
	providerPrivKeys, cosignerPrivKeys []*ecdsa.PrivateKey,
	rewardEpochId uint32,
) *types.FTDCProveResponse {
	cosignerAddresses, cosignerAndProvider := GetCosignerAddressesAndProvider(cosignerPrivKeys, providerPrivKeys)

	originalMessage := connector.IFtdcHubFtdcAttestationRequest{
		Header: connector.IFtdcHubFtdcRequestHeader{
			AttestationType:    [32]byte{},
			SourceId:           common.Hash{},
			ThresholdBIPS:      uint16(TotalWeight * 0.6),
			Cosigners:          cosignerAddresses,
			CosignersThreshold: uint64(len(cosignerAddresses)),
		},
		RequestBody: make([]byte, 10),
	}

	originalMessageEncoded, err := types.EncodeFTDCRequest(originalMessage)
	require.NoError(t, err)

	challenge, err := testutil.GenerateRandomBytes(32)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	additionalFixedMessageEncoded, variableMessages, privKeys, err := GetAdditionalFixedMessage(t, pc, challenge, originalMessage, timestamp, cosignerAndProvider, providerPrivKeys, cosignerPrivKeys)
	require.NoError(t, err)

	iData := utils.BuildInstructionData(t, constants.FTDC, constants.Prove, originalMessageEncoded, timestamp, additionalFixedMessageEncoded, nil, pc.TeeId, rewardEpochId)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts, instructions := utils.SignAndSendInstructionsWithAddVarMsgs(t, iData, variableMessages, privKeys, pc.ExtPort)

	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionId, types.Threshold, constants.FTDC, constants.Prove, pc.TeeId)

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeId)
	require.NoError(t, err)

	var ftdcResponse types.FTDCProveResponse
	err = json.Unmarshal(res.Result.Data, &ftdcResponse)
	require.NoError(t, err)

	// Verify FTDC response signatures
	ftdcMsgHash, _, err := types.HashFTDCMessage(originalMessage, additionalFixedMessageEncoded, timestamp)
	require.NoError(t, err)

	err = teeUtils.VerifySignature(ftdcMsgHash.Bytes(), ftdcResponse.TEESignature, pc.TeeId)
	require.NoError(t, err)

	require.Equal(t, len(ftdcResponse.DataProviderSignatures), len(providerPrivKeys))
	for i, signature := range ftdcResponse.DataProviderSignatures {
		err = teeUtils.VerifySignature(ftdcMsgHash.Bytes(), signature, crypto.PubkeyToAddress(providerPrivKeys[i].PublicKey))
		require.NoError(t, err)
	}

	require.Equal(t, len(ftdcResponse.CosignerSignatures), len(cosignerPrivKeys))
	for _, signature := range ftdcResponse.CosignerSignatures {
		_, err = teeUtils.CheckSignature(ftdcMsgHash.Bytes(), signature, cosignerAddresses)
		require.NoError(t, err)
	}

	require.Equal(t, ftdcResponse.ResponseBody, hexutil.Bytes(additionalFixedMessageEncoded))

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionId, constants.FTDC, constants.Prove, receipts)

	return &ftdcResponse
}

func GetCosignerAddressesAndProvider(cosignerPrivKeys []*ecdsa.PrivateKey, providerPrivKeys []*ecdsa.PrivateKey) ([]common.Address, map[common.Address]bool) {
	cosignerAddresses := make([]common.Address, len(cosignerPrivKeys))
	cosignerAndProvider := make(map[common.Address]bool)

	for j, cosignerPrivKey := range cosignerPrivKeys {
		cosignerAddresses[j] = teeUtils.PubkeyToAddress(&cosignerPrivKey.PublicKey)
		for _, providerPrivKey := range providerPrivKeys {
			if cosignerAddresses[j] == crypto.PubkeyToAddress(providerPrivKey.PublicKey) {
				cosignerAndProvider[cosignerAddresses[j]] = true
			}
		}
	}
	return cosignerAddresses, cosignerAndProvider
}

// GetAdditionalFixedMessage returns the additional fixed message, the variable messages (signatures) and the private keys for the provider and cosigner
func GetAdditionalFixedMessage(t *testing.T, pc *utils.ProxyConfig, challenge []byte, originalMessage connector.IFtdcHubFtdcAttestationRequest, timestamp uint64, cosignerAndProvider map[common.Address]bool, providerPrivKeys []*ecdsa.PrivateKey, cosignerPrivKeys []*ecdsa.PrivateKey) ([]byte, []hexutil.Bytes, []*ecdsa.PrivateKey, error) {
	additionalFixedMessage := verification.ITeeVerificationTeeAttestation{
		TeeMachine: verification.ITeeMachineRegistryTeeMachineWithAttestationData{
			TeeId:        pc.TeeId,
			InitialTeeId: common.Address{},
			Url:          "blabla",
			CodeHash:     [32]byte{},
			Platform:     [32]byte{},
		},
		Challenge: [32]byte(challenge),
	}

	additionalFixedMessageEncoded, err := types.EncodeTeeAttestationRequest(&additionalFixedMessage)
	require.NoError(t, err)

	ftdcMsgHash, _, err := types.HashFTDCMessage(originalMessage, additionalFixedMessageEncoded, timestamp)
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
		if _, check := cosignerAndProvider[teeUtils.PubkeyToAddress(&privKey.PublicKey)]; check {
			continue
		}
		variableMessage, err := teeUtils.Sign(ftdcMsgHash[:], privKey)
		require.NoError(t, err)

		variableMessages = append(variableMessages, variableMessage)
		privKeys = append(privKeys, privKey)
	}

	return additionalFixedMessageEncoded, variableMessages, privKeys, nil
}
