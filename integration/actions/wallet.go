package actions

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/backup"
	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	commonwallet "github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/stretchr/testify/require"
)

func GenerateWallet(
	t *testing.T,
	pc *utils.ProxyConfig,
	teeId common.Address,
	walletId [32]byte,
	keyId uint64,
	privKeys []*ecdsa.PrivateKey,
	adminWalletPublicKeys []commonwallet.PublicKey,
	rewardEpochId uint32,
) *commonwallet.ITeeWalletKeyManagerKeyExistence {
	originalMessage := commonwallet.ITeeWalletKeyManagerKeyGenerate{
		TeeId:    teeId,
		WalletId: walletId,
		KeyId:    keyId,
		OpType:   constants.XRP.Hash(),
		ConfigConstants: commonwallet.ITeeWalletKeyManagerKeyConfigConstants{
			OpTypeConstants:    make([]byte, 0),
			AdminsPublicKeys:   adminWalletPublicKeys,
			AdminsThreshold:    uint64(len(adminWalletPublicKeys)),
			Cosigners:          make([]common.Address, 0), // todo: add cosigners
			CosignersThreshold: 0,
		},
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[constants.KeyGenerate]}.Pack(originalMessage)
	require.NoError(t, err)

	iData, err := utils.BuildInstructionData(constants.Wallet, constants.KeyGenerate, originalMessageEncoded, nil, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	fmt.Printf("pc.Vc.ProposalExpiration: %v\n", pc.Vc.ProposalExpiration)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)

	utils.VerifyReceipts(t, receipts, iData)

	fmt.Printf("iData.InstructionId: %v\n", common.Hash(iData.InstructionId))

	res := utils.GetActionResponse(t, pc.ExtPort, "result", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.Threshold, constants.Wallet.Hash(), constants.KeyGenerate.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, teeId)
	require.NoError(t, err)

	var swe types.WalletSignedKeyExistenceProof

	err = json.Unmarshal(res.Result.Data, &swe)
	require.NoError(t, err)

	walletExistenceProof, err := structs.Decode[commonwallet.ITeeWalletKeyManagerKeyExistence](commonwallet.KeyExistenceStructArg, swe.KeyExistence)
	require.NoError(t, err)

	wst := make(chan bool, 1)

	nkc := make(chan *types.ActionResult, 1)

	go pc.Ws.RunInfo(t.Context(), wst, nkc)
	nkc <- &res.Result

	time.Sleep(5000 * time.Millisecond)

	walletInfo, err := pc.Ws.WalletInfo(walletExistenceProof.WalletId)
	require.NoError(t, err, "getting wallet info")
	require.Equal(t, walletExistenceProof.WalletId, walletInfo.WalletId)
	require.Equal(t, walletExistenceProof.KeyId, walletInfo.KeyId)
	require.Equal(t, walletExistenceProof.AddressStr, walletInfo.AddressStr)

	<-endOfVotingTicker.C
	res = utils.GetActionResponse(t, pc.ExtPort, "rewarding-data", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.End, constants.Wallet.Hash(), constants.KeyGenerate.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, teeId)
	require.NoError(t, err)

	signerSequence := new(types.RewardingData)
	err = json.Unmarshal(res.Result.Data, &signerSequence)
	require.NoError(t, err)
	require.Equal(t, signerSequence.VoteSequence.VoteHash, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]))

	return &walletExistenceProof
}

func DeleteWallet(t *testing.T, pc *utils.ProxyConfig, walletId [32]byte, keyId uint64,
	privKeys []*ecdsa.PrivateKey, rewardEpochId uint32, nonce *big.Int) {
	originalMessage := commonwallet.ITeeWalletKeyManagerKeyDelete{
		TeeId:    pc.TeeId,
		WalletId: walletId,
		KeyId:    keyId,
		Nonce:    nonce,
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[constants.KeyDelete]}.Pack(originalMessage)
	require.NoError(t, err)

	iData, err := utils.BuildInstructionData(constants.Wallet, constants.KeyDelete, originalMessageEncoded, nil, nil, pc.TeeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.GetActionResponse(t, pc.ExtPort, "result", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.Threshold, constants.Wallet.Hash(), constants.KeyDelete.Hash())

	wst := make(chan bool, 1)
	go pc.Ws.RunInfo(t.Context(), wst, nil)
	wst <- true

	time.Sleep(1500 * time.Millisecond)

	_, err = pc.Ws.WalletInfo(walletId)
	require.Error(t, err)

	_, err = pc.Ws.KeyData(walletId, keyId)
	require.Error(t, err)

	<-endOfVotingTicker.C
	res = utils.GetActionResponse(t, pc.ExtPort, "rewarding-data", iData.InstructionId)
	utils.VerifyActionResponse(t, res, types.End, constants.Wallet.Hash(), constants.KeyDelete.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeId)
	require.NoError(t, err)

	rewData := new(types.RewardingData)
	err = json.Unmarshal(res.Result.Data, &rewData)
	require.NoError(t, err)
	require.Equal(t, rewData.VoteSequence.VoteHash, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]))

	err = teeUtils.VerifySignature(rewData.VoteSequence.VoteHash[:], rewData.Signature, pc.TeeId)
	require.NoError(t, err)
}

func RecoverWallet(t *testing.T, pc *utils.ProxyConfig, walletId [32]byte, keyId uint64, providersPrivKeys, adminsPrivKeys []*ecdsa.PrivateKey,
	rewardEpochId uint32, nonce *big.Int, walletBackup *backup.WalletBackup) *commonwallet.ITeeWalletKeyManagerKeyExistence {
	originalMessage := commonwallet.ITeeWalletBackupManagerKeyDataProviderRestore{
		TeeId:     pc.TeeId,
		BackupUrl: "blabla",
		Nonce:     nonce,
		BackupId: commonwallet.ITeeWalletBackupManagerBackupId{
			TeeId:         pc.TeeId,
			WalletId:      walletId,
			KeyId:         keyId,
			OpType:        constants.XRP.Hash(),
			PublicKey:     append(walletBackup.PublicKey.X[:], walletBackup.PublicKey.Y[:]...),
			RewardEpochId: big.NewInt(int64(rewardEpochId)),
			RandomNonce:   new(big.Int).SetBytes(walletBackup.RandomNonce[:]),
		},
	}

	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[constants.KeyDataProviderRestore]}.Pack(originalMessage)
	require.NoError(t, err)

	additionalFixedMessage := walletBackup.WalletBackupMetaData

	adminAndProvider := make(map[common.Address]int)
	for j, adminPrivKey := range adminsPrivKeys {
		address := crypto.PubkeyToAddress(adminPrivKey.PublicKey)
		for _, providerPrivKey := range providersPrivKeys {
			if address == crypto.PubkeyToAddress(providerPrivKey.PublicKey) {
				adminAndProvider[address] = j
			}
		}
	}

	teeEciesPubKey := ecies.ImportECDSAPublic(pc.ProxyPubKey)
	additionalVariableMessages := make([]interface{}, 0)
	privKeys := make([]*ecdsa.PrivateKey, 0)
	for i, privKey := range providersPrivKeys {
		keySplit, err := backup.DecryptSplit(walletBackup.ProviderEncryptedParts.Splits[i], privKey)
		require.NoError(t, err)

		address := crypto.PubkeyToAddress(privKey.PublicKey)
		j, check := adminAndProvider[address]
		var plaintext []byte
		if !check {
			plaintext, err = json.Marshal(keySplit)
			require.NoError(t, err)
		} else {
			keySplitAdmin, err := backup.DecryptSplit(walletBackup.AdminEncryptedParts.Splits[j], privKey)
			require.NoError(t, err)
			var twoKeySplits [2]backup.KeySplit
			twoKeySplits[0] = *keySplit
			twoKeySplits[1] = *keySplitAdmin
			plaintext, err = json.Marshal(twoKeySplits)
			require.NoError(t, err)
		}

		cipher, err := ecies.Encrypt(rand.Reader, teeEciesPubKey, plaintext, nil, nil)
		require.NoError(t, err)

		additionalVariableMessages = append(additionalVariableMessages, cipher)
		privKeys = append(privKeys, privKey)
	}

	for i, privKey := range adminsPrivKeys {
		address := crypto.PubkeyToAddress(privKey.PublicKey)
		_, check := adminAndProvider[address]
		if check {
			continue
		}

		keySplit, err := backup.DecryptSplit(walletBackup.AdminEncryptedParts.Splits[i], privKey)
		require.NoError(t, err)

		plaintext, err := json.Marshal(keySplit)
		require.NoError(t, err)

		cipher, err := ecies.Encrypt(rand.Reader, teeEciesPubKey, plaintext, nil, nil)
		require.NoError(t, err)

		additionalVariableMessages = append(additionalVariableMessages, cipher)
		privKeys = append(privKeys, privKey)
	}

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()

	instructionId, _ := utils.GenerateRandomBytes(32)
	receipts := make([]*voting.SignedReceipt, 0)
	instructions := make([]*instruction.Data, 0)
	for i, privKey := range privKeys {
		iData, err := utils.BuildInstructionDataWithId(common.BytesToHash(instructionId), constants.Wallet, constants.KeyDataProviderRestore, originalMessageEncoded, additionalFixedMessage, additionalVariableMessages[i], pc.TeeId, rewardEpochId)
		require.NoError(t, err)
		receipts = append(receipts, utils.SignAndSendInstruction(t, iData, privKey, pc.ExtPort))
		instructions = append(instructions, iData)
	}
	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.GetActionResponse(t, pc.ExtPort, "result", common.BytesToHash(instructionId))
	utils.VerifyActionResponse(t, res, types.Threshold, constants.Wallet.Hash(), constants.KeyDataProviderRestore.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeId)
	require.NoError(t, err)

	walletExistenceProof, err := types.ExtractKeyExistence(res.Result.Data)
	require.NoError(t, err)

	wst := make(chan bool, 1)
	go pc.Ws.RunInfo(t.Context(), wst, nil)
	wst <- true

	time.Sleep(1500 * time.Millisecond)

	// check that wallet is actually on the tee
	wallet, err := pc.Ws.WalletInfo(walletId)
	require.NoError(t, err)
	require.Equal(t, walletId, wallet.WalletId)
	require.Equal(t, keyId, wallet.KeyId)

	<-endOfVotingTicker.C
	res = utils.GetActionResponse(t, pc.ExtPort, "rewarding-data", common.BytesToHash(instructionId))
	utils.VerifyActionResponse(t, res, types.End, constants.Wallet.Hash(), constants.KeyDataProviderRestore.Hash())

	err = teeUtils.VerifySignature(crypto.Keccak256(res.Result.Data), res.Signature, pc.TeeId)
	require.NoError(t, err)

	rewData := new(types.RewardingData)
	err = json.Unmarshal(res.Result.Data, &rewData)
	require.NoError(t, err)
	require.Equal(t, rewData.VoteSequence.VoteHash, common.BytesToHash(receipts[len(receipts)-1].Receipt.VoteHash[:]))

	err = teeUtils.VerifySignature(rewData.VoteSequence.VoteHash[:], rewData.Signature, pc.TeeId)
	require.NoError(t, err)

	return walletExistenceProof
}
