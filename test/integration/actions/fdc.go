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
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/fdc2"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/verification"
	"github.com/flare-foundation/tee-node/pkg/fdc"
	"github.com/flare-foundation/tee-node/pkg/types"

	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/test/integration/utils"
	"github.com/stretchr/testify/require"
)

const TotalWeight = 10000

func FDCProve(
	t *testing.T,
	pc *utils.ProxyConfig,
	providerPrivKeys, cosignerPrivKeys []*ecdsa.PrivateKey,
	rewardEpochID uint32,
) *fdc.ProveResponse {
	t.Helper()

	cosignerAddresses, cosignerAndProvider := CosignerAddressesAndProvider(cosignerPrivKeys, providerPrivKeys)
	cosignersThreshold := uint64(len(cosignerAddresses))
	originalMessage := fdc2.IFdc2HubFdc2AttestationRequest{
		Header: fdc2.IFdc2HubFdc2RequestHeader{
			AttestationType: [32]byte{},
			SourceId:        common.Hash{},
			ThresholdBIPS:   uint16(TotalWeight * 0.6),
		},
		RequestBody: make([]byte, 10),
	}

	chainID := utils.TestChainID

	originalMessageEncoded, err := fdc.EncodeRequest(originalMessage)
	require.NoError(t, err)

	challenge, err := random.Hash()
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	additionalFixedMessageEncoded, variableMessages, privKeys, err := GetAdditionalFixedMessage(t, pc, challenge, originalMessage, timestamp, cosignerAndProvider, providerPrivKeys, cosignerPrivKeys, cosignerAddresses, cosignersThreshold, chainID)
	require.NoError(t, err)

	iData := utils.BuildInstructionData(t, op.FDC2, op.Prove, originalMessageEncoded, timestamp, additionalFixedMessageEncoded, nil, cosignerAddresses, cosignersThreshold, pc.TeeID, rewardEpochID)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts, instructions := utils.SignAndSendInstructionsWithAddVarMsgs(t, iData, variableMessages, privKeys, pc.ExtPort)

	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.FDC2, op.Prove, pc.TeeID, 1)

	err = teeUtils.VerifySignature(utils.ActionResultSignHash(t, res.Result.Hash()), res.Signature, pc.TeeID)
	require.NoError(t, err)

	var fdcResponse fdc.ProveResponse
	err = json.Unmarshal(res.Result.Data, &fdcResponse)
	require.NoError(t, err)

	// Verify FDC response signatures. TEE signs the raw messageHash; cosigner
	// signatures are over the Relay Mode-2 prefixed hash (matches
	// Verification.toCosignersMessageHash on chain + Relay.relay()).
	msgHash, _, err := fdc.HashMessage(chainID, originalMessage, additionalFixedMessageEncoded, cosignerAddresses, cosignersThreshold, timestamp)
	require.NoError(t, err)
	cosignerSigningHash := fdc.RelayPrefixedHash(msgHash)

	err = teeUtils.VerifySignature(msgHash.Bytes(), fdcResponse.TEESignature, pc.TeeID)
	require.NoError(t, err)

	require.Equal(t, len(fdcResponse.CosignerSignatures), len(cosignerPrivKeys))
	for _, signature := range fdcResponse.CosignerSignatures {
		_, err = teeUtils.CheckSignature(cosignerSigningHash.Bytes(), signature, cosignerAddresses)
		require.NoError(t, err)
	}

	require.Equal(t, fdcResponse.ResponseBody, hexutil.Bytes(additionalFixedMessageEncoded))

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.FDC2, op.Prove, receipts)

	return &fdcResponse
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
func GetAdditionalFixedMessage(t *testing.T, pc *utils.ProxyConfig, challenge [32]byte, originalMessage fdc2.IFdc2HubFdc2AttestationRequest, timestamp uint64, cosignerAndProvider map[common.Address]bool, providerPrivKeys []*ecdsa.PrivateKey, cosignerPrivKeys []*ecdsa.PrivateKey, cosignerAddresses []common.Address, cosignersThreshold uint64, chainID uint64) ([]byte, []hexutil.Bytes, []*ecdsa.PrivateKey, error) {
	t.Helper()

	additionalFixedMessage := verification.IVerificationTeeAttestation{
		TeeMachine: verification.IMachineManagerTeeMachineWithAttestationData{
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

	fdcMsgHash, _, err := fdc.HashMessage(chainID, originalMessage, additionalFixedMessageEncoded, cosignerAddresses, cosignersThreshold, timestamp)
	require.NoError(t, err)
	// Data providers + cosigners both sign the Relay Mode-2 prefixed hash.
	dpSigningHash := fdc.RelayPrefixedHash(fdcMsgHash)

	variableMessages := make([]hexutil.Bytes, 0)
	privKeys := make([]*ecdsa.PrivateKey, 0)
	for _, privKey := range providerPrivKeys {
		variableMessage, err := teeUtils.Sign(dpSigningHash[:], privKey)
		require.NoError(t, err)

		variableMessages = append(variableMessages, variableMessage)
		privKeys = append(privKeys, privKey)
	}
	for _, privKey := range cosignerPrivKeys {
		if _, check := cosignerAndProvider[crypto.PubkeyToAddress(privKey.PublicKey)]; check {
			continue
		}
		variableMessage, err := teeUtils.Sign(dpSigningHash[:], privKey)
		require.NoError(t, err)

		variableMessages = append(variableMessages, variableMessage)
		privKeys = append(privKeys, privKey)
	}

	return additionalFixedMessageEncoded, variableMessages, privKeys, nil
}
