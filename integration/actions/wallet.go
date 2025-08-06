package actions

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/backup"
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

// GenerateWallet Sends KEY_GENERATE instruction for wallet with specified admins, verifies ITeeWalletKeyManagerKeyExistence proof  and checks that wallet is present in proxy wallet storage
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

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, constants.Wallet, constants.KeyGenerate, originalMessageEncoded, timestamp, nil, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)

	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionId, types.Threshold, constants.Wallet, constants.KeyGenerate, teeId)

	var swe types.WalletSignedKeyExistenceProof

	err = json.Unmarshal(res.Result.Data, &swe)
	require.NoError(t, err)

	walletExistenceProof, err := structs.Decode[commonwallet.ITeeWalletKeyManagerKeyExistence](commonwallet.KeyExistenceStructArg, swe.KeyExistence)
	require.NoError(t, err)

	wst := make(chan bool, 1)

	nkc := make(chan *types.ActionResult, 1)

	go pc.Ws.RunInfo(t.Context(), wst, nkc)
	nkc <- &res.Result

	walletInfo := utils.GetWalletInfo(t, pc, walletId, keyId)
	require.NoError(t, err)
	require.Equal(t, walletExistenceProof.WalletId, walletInfo.Info.WalletId)
	require.Equal(t, walletExistenceProof.KeyId, walletInfo.Info.KeyId)
	require.Equal(t, walletExistenceProof.AddressStr, walletInfo.Info.AddressStr)
	require.Equal(t, constants.XRP.Hash(), common.BytesToHash(originalMessage.OpType[:]))
	require.Equal(t, originalMessage.ConfigConstants, walletExistenceProof.ConfigConstants)
	require.Equal(t, false, walletExistenceProof.Restored)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionId, constants.Wallet, constants.KeyGenerate, receipts)

	votingStatus := utils.GetVotingStatus(t, pc, rewardEpochId, iData.InstructionId)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, utils.TotalWeight/2)

	return &walletExistenceProof
}

// DeleteWallet Send KEY_DELETE instruction, verifies response and checks that the wallet is deleted from proxy wallet storage
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

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, constants.Wallet, constants.KeyDelete, originalMessageEncoded, timestamp, nil, nil, pc.TeeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionId, types.Threshold, constants.Wallet, constants.KeyDelete, pc.TeeId)

	wst := make(chan bool, 1)
	go pc.Ws.RunInfo(t.Context(), wst, nil)
	wst <- true

	time.Sleep(1500 * time.Millisecond)

	// Check that the wallet is removed from proxy
	_, err = pc.Ws.WalletInfo(walletId)
	require.Error(t, err)

	_, err = pc.Ws.KeyData(walletId, keyId)
	require.Error(t, err)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionId, constants.Wallet, constants.KeyDelete, receipts)

	votingStatus := utils.GetVotingStatus(t, pc, rewardEpochId, iData.InstructionId)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, utils.TotalWeight/2)
}

// RecoverWallet Recovers providers & admins wallet shares, sends KEY_DATA_PROVIDER_RESTORE instruction, verifies ITeeWalletKeyManagerKeyExistence proof and cheks that recovered wallet is in proxy wallet storage
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
	addVarMsgs := make([]interface{}, 0)
	privKeys := make([]*ecdsa.PrivateKey, 0)
	// Recover providers shares
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

		addVarMsgs = append(addVarMsgs, cipher)
		privKeys = append(privKeys, privKey)
	}

	// Recover admin shares
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

		addVarMsgs = append(addVarMsgs, cipher)
		privKeys = append(privKeys, privKey)
	}

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()

	// Send KEY_DATA_PROVIDER_RESTORE instruction for each provider & admin
	instructionId, _ := utils.GenerateRandomBytes(32)
	receipts := make([]*voting.SignedReceipt, 0)
	instructions := make([]instruction.Data, 0)
	for i, privKey := range privKeys {
		timestamp := uint64(time.Now().Unix())
		iData := utils.BuildInstructionDataWithId(t, common.BytesToHash(instructionId), constants.Wallet, constants.KeyDataProviderRestore,
			originalMessageEncoded, timestamp, additionalFixedMessage, addVarMsgs[i], pc.TeeId, rewardEpochId)
		receipts = append(receipts, utils.SignAndSendInstruction(t, iData, privKey, pc.ExtPort))
		instructions = append(instructions, *iData)
	}
	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", common.BytesToHash(instructionId), types.Threshold, constants.Wallet, constants.KeyDataProviderRestore, pc.TeeId)

	walletExistenceProof, err := types.ExtractKeyExistence(res.Result.Data)
	require.NoError(t, err)

	wst := make(chan bool, 1)
	go pc.Ws.RunInfo(t.Context(), wst, nil)
	wst <- true

	// Check that wallet is actually on the tee
	walletInfo := utils.GetWalletInfo(t, pc, walletId, keyId)
	require.NoError(t, err)
	require.Equal(t, walletId, walletInfo.Info.WalletId)
	require.Equal(t, keyId, walletInfo.Info.KeyId)
	require.Equal(t, true, walletExistenceProof.Restored)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, common.BytesToHash(instructionId), constants.Wallet, constants.KeyDataProviderRestore, receipts)

	votingStatus := utils.GetVotingStatus(t, pc, rewardEpochId, common.BytesToHash(instructionId))
	utils.VerifyVotingStatus(t, votingStatus, uint16(len(adminsPrivKeys)), uint16(len(adminsPrivKeys)), utils.TotalWeight/2)

	return walletExistenceProof
}
