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
	"github.com/flare-foundation/go-flare-common/pkg/random"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/wallets/backup"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/voting"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	commonwallet "github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/test/integration/utils"
	"github.com/stretchr/testify/require"
)

// GenerateWallet Sends KEY_GENERATE instruction for wallet with specified admins, verifies IWalletKeyManagerKeyExistence proof and checks that wallet is present in proxy wallet storage.
func GenerateWallet(
	t *testing.T,
	pc *utils.ProxyConfig,
	teeID common.Address,
	walletID [32]byte,
	keyID uint64,
	privKeys []*ecdsa.PrivateKey,
	adminWalletPublicKeys []commonwallet.PublicKey,
	rewardEpochID uint32,
) *commonwallet.IWalletKeyManagerKeyExistence {
	t.Helper()

	originalMessage := commonwallet.IWalletKeyManagerKeyGenerate{
		TeeId:       teeID,
		WalletId:    walletID,
		KeyId:       keyID,
		KeyType:     wallets.XRPType,
		SigningAlgo: wallets.XRPSignAlgo,
		ConfigConstants: commonwallet.IWalletKeyManagerKeyConfigConstants{
			AdminsPublicKeys:   adminWalletPublicKeys,
			AdminsThreshold:    uint64(len(adminWalletPublicKeys)),
			Cosigners:          make([]common.Address, 0), // todo: add cosigners
			CosignersThreshold: 0,
		},
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[op.KeyGenerate]}.Pack(originalMessage)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, op.Wallet, op.KeyGenerate, originalMessageEncoded, timestamp, nil, nil, nil, 0, teeID, rewardEpochID)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)

	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.Wallet, op.KeyGenerate, teeID, 1)

	require.Equal(t, uint8(1), res.Result.Status, res.Result.Log)
	var swe wallets.SignedKeyExistenceProof

	err = json.Unmarshal(res.Result.Data, &swe)
	require.NoError(t, err)

	walletExistenceProof, err := structs.Decode[commonwallet.IWalletKeyManagerKeyExistence](commonwallet.KeyExistenceStructArg, swe.KeyExistence)
	require.NoError(t, err)

	wst := make(chan bool, 1)

	nkc := make(chan *types.ActionResult, 1)
	btrig := make(chan bool, 1)

	go pc.Ws.RunUpdateInfo(t.Context(), wst, btrig, nkc, nil)
	nkc <- &res.Result

	time.Sleep(500 * time.Millisecond)

	walletInfo := utils.GetWalletInfo(t, pc, walletID, keyID)
	require.NoError(t, err)
	require.Equal(t, common.Hash(walletExistenceProof.WalletId), walletInfo.Info.WalletID)
	require.Equal(t, walletExistenceProof.KeyId, walletInfo.Info.KeyID)
	// require.Equal(t, op.XRP.Hash(), common.BytesToHash(originalMessage.OpType[:]))
	require.Equal(t, originalMessage.ConfigConstants, walletExistenceProof.ConfigConstants)
	require.Equal(t, false, walletExistenceProof.Restored)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.Wallet, op.KeyGenerate, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochID, iData.InstructionID)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, testutil.TotalWeight/2)

	return &walletExistenceProof
}

// DeleteWallet Send KEY_DELETE instruction, verifies response and checks that the wallet is deleted from proxy wallet storage
func DeleteWallet(
	t *testing.T,
	pc *utils.ProxyConfig,
	walletID common.Hash,
	keyID uint64,
	privKeys []*ecdsa.PrivateKey,
	rewardEpochID uint32,
	nonce *big.Int,
) {
	t.Helper()

	originalMessage := commonwallet.IWalletKeyManagerKeyDelete{
		TeeId:    pc.TeeID,
		WalletId: walletID,
		KeyId:    keyID,
		Nonce:    nonce,
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[op.KeyDelete]}.Pack(originalMessage)
	require.NoError(t, err)

	timestamp := uint64(time.Now().Unix())
	iData := utils.BuildInstructionData(t, op.Wallet, op.KeyDelete, originalMessageEncoded, timestamp, nil, nil, nil, 0, pc.TeeID, rewardEpochID)
	require.NoError(t, err)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := utils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)
	utils.VerifyReceipts(t, receipts, iData)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, op.Wallet, op.KeyDelete, pc.TeeID, 1)

	wst := make(chan bool, 1)
	keyActions := make(chan *types.ActionResult, 1)
	go pc.Ws.RunUpdateInfo(t.Context(), wst, nil, keyActions, nil)
	keyActions <- &res.Result

	time.Sleep(1500 * time.Millisecond)

	// Check that the wallet is removed from proxy
	_, err = pc.Ws.WalletInfo(walletID)
	require.Error(t, err)

	_, err = pc.Ws.KeyData(walletID, keyID)
	require.Error(t, err)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, iData.InstructionID, op.Wallet, op.KeyDelete, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochID, iData.InstructionID)
	utils.VerifyVotingStatus(t, votingStatus, 0, 0, testutil.TotalWeight/2)
}

// RecoverWallet Recovers providers & admins wallet shares, sends KEY_DATA_PROVIDER_RESTORE instruction, verifies IWalletKeyManagerKeyExistence proof and checks that recovered wallet is in proxy wallet storage.
func RecoverWallet(
	t *testing.T,
	pc *utils.ProxyConfig,
	walletID common.Hash,
	keyID uint64,
	providersPrivKeys, adminsPrivKeys []*ecdsa.PrivateKey,
	rewardEpochID uint32,
	nonce *big.Int,
	walletBackup *backup.WalletBackup,
) *commonwallet.IWalletKeyManagerKeyExistence {
	t.Helper()

	tpk := types.PubKeyToStruct(pc.TeePubKey)
	teePK := commonwallet.PublicKey{
		X: tpk.X,
		Y: tpk.Y,
	}

	originalMessage := commonwallet.IWalletBackupManagerKeyDataProviderRestore{
		TeePublicKey: teePK,
		BackupUrl:    "blabla",
		Nonce:        nonce,
		BackupId: commonwallet.IWalletBackupManagerBackupId{
			TeeId:         pc.TeeID,
			WalletId:      walletID,
			KeyId:         keyID,
			KeyType:       wallets.XRPType,
			SigningAlgo:   wallets.XRPSignAlgo,
			PublicKey:     walletBackup.PublicKey,
			RewardEpochId: rewardEpochID,
			RandomNonce:   walletBackup.RandomNonce,
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
	addVarMsgs := make([]any, 0, len(providersPrivKeys)+len(adminsPrivKeys))
	privKeys := make([]*ecdsa.PrivateKey, 0, len(providersPrivKeys)+len(adminsPrivKeys))
	// Recover providers shares
	for i, privKey := range providersPrivKeys {
		keySplit, err := backup.DecryptSplit(walletBackup.ProviderEncryptedParts.Splits[i], privKey, utils.TestChainID)
		require.NoError(t, err)

		address := crypto.PubkeyToAddress(privKey.PublicKey)
		j, check := adminAndProvider[address]
		var plaintext []byte
		if !check {
			plaintext, err = json.Marshal(keySplit)
			require.NoError(t, err)
		} else {
			keySplitAdmin, err := backup.DecryptSplit(walletBackup.AdminEncryptedParts.Splits[j], privKey, utils.TestChainID)
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

		keySplit, err := backup.DecryptSplit(walletBackup.AdminEncryptedParts.Splits[i], privKey, utils.TestChainID)
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

	instructionID, err := random.Hash()
	require.NoError(t, err)

	receipts := make([]*voting.SignedReceipt, 0, len(privKeys))
	instructions := make([]instruction.Data, 0, len(privKeys))
	timestamp := uint64(time.Now().Unix())
	for i, privKey := range privKeys {
		iData := utils.BuildInstructionDataWithID(t, instructionID, op.Wallet, op.KeyDataProviderRestore,
			originalMessageEncoded, timestamp, additionalFixedMessage, addVarMsgs[i], adminAddresses, adminsThreshold, pc.TeeID, rewardEpochID)
		receipts = append(receipts, utils.SignAndSendInstruction(t, iData, privKey, pc.ExtPort))
		instructions = append(instructions, *iData)
	}
	utils.VerifyReceiptsForMultipleInstructions(t, receipts, instructions)

	res := utils.FetchAndVerifyActionResponse(t, pc.ExtPort, instructionID, types.Threshold, op.Wallet, op.KeyDataProviderRestore, pc.TeeID, 1)

	walletExistenceProof, err := wallets.ExtractKeyExistence(res.Result.Data, pc.TeeID, utils.TestChainID)
	require.NoError(t, err)

	wst := make(chan bool, 1)
	keyActions := make(chan *types.ActionResult, 1)
	go pc.Ws.RunUpdateInfo(t.Context(), wst, nil, keyActions, nil)
	keyActions <- &res.Result

	time.Sleep(500 * time.Millisecond)

	// Check that wallet is actually on the tee
	walletInfo := utils.GetWalletInfo(t, pc, walletID, keyID)
	require.NoError(t, err)
	require.Equal(t, walletID, walletInfo.Info.WalletID)
	require.Equal(t, keyID, walletInfo.Info.KeyID)
	require.Equal(t, true, walletExistenceProof.Restored)

	<-endOfVotingTicker.C
	utils.FetchAndVerifyRewardingData(t, pc, instructionID, op.Wallet, op.KeyDataProviderRestore, receipts)

	votingStatus := utils.GetVotingStatuses(t, pc, rewardEpochID, instructionID)
	utils.VerifyVotingStatus(t, votingStatus, uint16(len(adminsPrivKeys)), uint16(len(adminsPrivKeys)), testutil.TotalWeight/2)

	return walletExistenceProof
}
