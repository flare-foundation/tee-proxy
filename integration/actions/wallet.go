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
	"github.com/flare-foundation/tee-node/pkg/wallets/backup"
	"github.com/flare-foundation/tee-proxy/pkg/voting"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	commonwallet "github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
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
		TeeId:       teeId,
		WalletId:    walletId,
		KeyId:       keyId,
		KeyType:     wallets.XRPType,
		SigningAlgo: wallets.XRPAlgo,
		ConfigConstants: commonwallet.ITeeWalletKeyManagerKeyConfigConstants{
			AdminsPublicKeys:   adminWalletPublicKeys,
			AdminsThreshold:    uint64(len(adminWalletPublicKeys)),
			Cosigners:          make([]common.Address, 0), // todo: add cosigners
			CosignersThreshold: 0,
		},
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[op.KeyGenerate]}.Pack(originalMessage)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, op.Wallet, op.KeyGenerate, originalMessageEncoded, timestamp, nil, nil, nil, 0, teeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)

	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionID, types.Threshold, op.Wallet, op.KeyGenerate, teeId)

	require.Equal(t, uint8(1), res.Result.Status, res.Result.Log)
	var swe wallets.SignedKeyExistenceProof

	err = json.Unmarshal(res.Result.Data, &swe)
	require.NoError(t, err)

	walletExistenceProof, err := structs.Decode[commonwallet.ITeeWalletKeyManagerKeyExistence](commonwallet.KeyExistenceStructArg, swe.KeyExistence)
	require.NoError(t, err)

	wst := make(chan bool, 1)

	nkc := make(chan *types.ActionResult, 1)
	btrig := make(chan bool, 1)

	go pc.Ws.RunInfo(t.Context(), wst, btrig, nkc)
	nkc <- &res.Result

	time.Sleep(500 * time.Millisecond)

	walletInfo := utils.GetWalletInfo(t, pc, walletId, keyId)
	require.NoError(t, err)
	require.Equal(t, common.Hash(walletExistenceProof.WalletId), walletInfo.Info.WalletID)
	require.Equal(t, walletExistenceProof.KeyId, walletInfo.Info.KeyID)
	// require.Equal(t, op.XRP.Hash(), common.BytesToHash(originalMessage.OpType[:]))
	require.Equal(t, originalMessage.ConfigConstants, walletExistenceProof.ConfigConstants)
	require.Equal(t, false, walletExistenceProof.Restored)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.Wallet, op.KeyGenerate, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochId, iData.InstructionID)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, testutil.TotalWeight/2)

	return &walletExistenceProof
}

// DeleteWallet Send KEY_DELETE instruction, verifies response and checks that the wallet is deleted from proxy wallet storage
func DeleteWallet(t *testing.T, pc *utils.ProxyConfig, walletID common.Hash, keyID uint64,
	privKeys []*ecdsa.PrivateKey, rewardEpochId uint32, nonce *big.Int) {
	originalMessage := commonwallet.ITeeWalletKeyManagerKeyDelete{
		TeeId:    pc.TeeId,
		WalletId: walletID,
		KeyId:    keyID,
		Nonce:    nonce,
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[op.KeyDelete]}.Pack(originalMessage)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, op.Wallet, op.KeyDelete, originalMessageEncoded, timestamp, nil, nil, nil, 0, pc.TeeId, rewardEpochId)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", iData.InstructionID, types.Threshold, op.Wallet, op.KeyDelete, pc.TeeId)

	wst := make(chan bool, 1)
	go pc.Ws.RunInfo(t.Context(), wst, nil, nil)
	wst <- true

	time.Sleep(1500 * time.Millisecond)

	// Check that the wallet is removed from proxy
	_, err = pc.Ws.WalletInfo(walletID)
	require.Error(t, err)

	_, err = pc.Ws.KeyData(walletID, keyID)
	require.Error(t, err)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.Wallet, op.KeyDelete, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochId, iData.InstructionID)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, testutil.TotalWeight/2)
}

// RecoverWallet Recovers providers & admins wallet shares, sends KEY_DATA_PROVIDER_RESTORE instruction, verifies ITeeWalletKeyManagerKeyExistence proof and checks that recovered wallet is in proxy wallet storage.
func RecoverWallet(
	t *testing.T,
	pc *utils.ProxyConfig,
	walletID common.Hash,
	keyID uint64,
	providersPrivKeys, adminsPrivKeys []*ecdsa.PrivateKey,
	rewardEpochId uint32,
	nonce *big.Int,
	walletBackup *backup.WalletBackup,
) *commonwallet.ITeeWalletKeyManagerKeyExistence {
	tpk := types.PubKeyToStruct(pc.TeePubKey)
	teePK := commonwallet.PublicKey{
		X: tpk.X,
		Y: tpk.Y,
	}

	originalMessage := commonwallet.ITeeWalletBackupManagerKeyDataProviderRestore{
		TeePublicKey: teePK,
		BackupUrl:    "blabla",
		Nonce:        nonce,
		BackupId: commonwallet.ITeeWalletBackupManagerBackupId{
			TeeId:         pc.TeeId,
			WalletId:      walletID,
			KeyId:         keyID,
			KeyType:       wallets.XRPType,
			SigningAlgo:   wallets.XRPAlgo,
			PublicKey:     append(walletBackup.PublicKey.X[:], walletBackup.PublicKey.Y[:]...),
			RewardEpochId: big.NewInt(int64(rewardEpochId)),
			RandomNonce:   new(big.Int).SetBytes(walletBackup.RandomNonce[:]),
		},
	}

	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[op.KeyDataProviderRestore]}.Pack(originalMessage)
	require.NoError(t, err)

	additionalFixedMessage := walletBackup.WalletBackupMetaData

	adminAndProvider := make(map[common.Address]int)
	adminAddresses := make([]common.Address, len(adminsPrivKeys))
	for j, adminPrivKey := range adminsPrivKeys {
		address := crypto.PubkeyToAddress(adminPrivKey.PublicKey)
		for _, providerPrivKey := range providersPrivKeys {
			if address == crypto.PubkeyToAddress(providerPrivKey.PublicKey) {
				adminAndProvider[address] = j
			}
		}
		adminAddresses[j] = address
	}
	adminsThreshold := uint64(len(adminAddresses))

	teeEciesPubKey := ecies.ImportECDSAPublic(pc.ProxyPubKey)
	addVarMsgs := make([]any, 0)
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

	instructionID, _ := testutil.GenerateRandomBytes(32)
	receipts := make([]*voting.SignedReceipt, 0)
	instructions := make([]instruction.Data, 0)
	for i, privKey := range privKeys {
		timestamp := uint64(time.Now().Unix())
		iData := utils.BuildInstructionDataWithId(t, common.BytesToHash(instructionID), op.Wallet, op.KeyDataProviderRestore,
			originalMessageEncoded, timestamp, additionalFixedMessage, addVarMsgs[i], adminAddresses, adminsThreshold, pc.TeeId, rewardEpochId)
		receipts = append(receipts, utils.SignAndSendInstruction(t, iData, privKey, pc.ExtPort))
		instructions = append(instructions, *iData)
	}
	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, "result", common.BytesToHash(instructionID), types.Threshold, op.Wallet, op.KeyDataProviderRestore, pc.TeeId)

	walletExistenceProof, err := wallets.ExtractKeyExistence(res.Result.Data)
	require.NoError(t, err)

	wst := make(chan bool, 1)
	go pc.Ws.RunInfo(t.Context(), wst, nil, nil)
	wst <- true

	// Check that wallet is actually on the tee
	walletInfo := utils.GetWalletInfo(t, pc, walletID, keyID)
	require.NoError(t, err)
	require.Equal(t, walletID, walletInfo.Info.WalletID)
	require.Equal(t, keyID, walletInfo.Info.KeyID)
	require.Equal(t, true, walletExistenceProof.Restored)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, common.BytesToHash(instructionID), op.Wallet, op.KeyDataProviderRestore, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochId, common.BytesToHash(instructionID))
	utils.VerifyVotingStatus(t, votingStatus, uint16(len(adminsPrivKeys)), uint16(len(adminsPrivKeys)), testutil.TotalWeight/2)

	return walletExistenceProof
}
