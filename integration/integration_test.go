package integration

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/tee"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/verification"
	"github.com/flare-foundation/tee-node/pkg/backup"
	"github.com/flare-foundation/tee-proxy/pkg/config"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payment"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/tee-node/pkg/utils"

	"github.com/alicebob/miniredis/v2"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"github.com/flare-foundation/go-flare-common/pkg/policy"
	commonwallet "github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	instructionService "github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/testutil"

	"github.com/stretchr/testify/require"

	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/service/action"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/service/server"
	"github.com/flare-foundation/tee-proxy/pkg/info"
	"github.com/flare-foundation/tee-proxy/pkg/meta"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
)

type testServers struct {
	external      *server.External
	internal      *server.Internal
	resultService *result.Service
}

type proxyConfig struct {
	extPort uint
	intPort uint
	teeId   common.Address
	proxyPk *ecdsa.PrivateKey
	aq      *queue.ActionQueues
	rs      *queue.ResponseStorage
	vc      *voting.Config
	vs      *voting.Storage
	is      *instructionService.Service
	ws      *wallets.Storage
}

var testTimeConfig = struct {
	Timeout  time.Duration
	Interval time.Duration
}{
	Timeout:  1000 * time.Millisecond,
	Interval: 50 * time.Millisecond,
}

func newProxyConfig(t *testing.T, externalPort, internalPort uint, proxyPk *ecdsa.PrivateKey, teeId common.Address, vc *voting.Config) (*proxyConfig, func()) {
	mr := miniredis.RunT(t)
	c := queue.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c)
	rs := queue.NewResultStorage(c)

	// TODO extract this
	walletStorage := wallets.NewStorage(aq, rs)
	// TODO - probably we can mock it for some simple tests
	metaObj := meta.New(&walletStorage)

	vs := voting.NewStorage(vc, 3, metaObj, 3)

	cleanup := func() {
		logger.Info("Flushing redis")
		c.FlushAll(t.Context())
		_ = c.Close()
		mr.Close()
	}

	// TODO I think the flow should be changed: first we create internal server, then communicate the TEE Address from TEE to the proxy, then we create external server??
	is := instructionService.NewService(teeId, proxyPk, make(chan policy.SigningPolicy, 1), aq, vs)

	return &proxyConfig{
		extPort: externalPort,
		intPort: internalPort,
		proxyPk: proxyPk,
		teeId:   teeId,
		aq:      aq,
		rs:      rs,
		vc:      vc,
		vs:      vs,
		ws:      &walletStorage,
		is:      &is,
	}, cleanup
}

func runProxyServers(t *testing.T, cfg *proxyConfig) *testServers {
	t.Helper()

	// Setup action and result services
	actionService := action.NewService(cfg.aq)
	resultService := result.NewService(cfg.rs)

	// TODO Setup info storage (only exported fields)
	infoStorage := &info.Storage{
		Latest: nil, // default
	}

	external := server.NewExternal(fmt.Sprintf("%d", cfg.extPort), cfg.is, &actionService, &resultService, infoStorage, cfg.ws)
	internal := server.NewInternal(fmt.Sprintf("%d", cfg.intPort), &actionService, &resultService, cfg.ws)

	go func() {
		err := cfg.is.Forward(t.Context())
		if err != nil {
			return
		}
	}()

	go func() {
		logger.Info("Starting external server")
		err := external.Serve()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("server error: %v", err)
		}
	}()
	go func() {
		logger.Info("Starting internal server")
		err := internal.Serve()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("server error: %v", err)
		}
	}()

	return &testServers{
		external:      external,
		internal:      internal,
		resultService: &resultService,
	}
}

func TestInitializeSigningPolicy(t *testing.T) {
	// Start of setup
	const extPort = 8000
	const intPort = 8001

	numVoters, _, startingEpochId := 100, 10, uint32(1)
	GenerateRandomKeys(numVoters)

	numAdmins := 3
	adminPubKeys := make([]*ecdsa.PublicKey, numAdmins)
	adminPrivKeys := make([]*ecdsa.PrivateKey, numAdmins)

	var err error
	for i := range numAdmins {
		adminPrivKeys[i], err = crypto.GenerateKey()
		require.NoError(t, err)
		adminPubKeys[i] = &adminPrivKeys[i].PublicKey
	}

	// change type
	adminWalletPublicKeys := make([]commonwallet.PublicKey, len(adminPubKeys))
	for i, pubKey := range adminPubKeys {
		adminWalletPublicKeys[i] = commonwallet.PublicKey(types.PubKeyToStruct(pubKey))
	}

	//todo: set teeId
	cfg, redisCleanup := newProxyConfig(t, extPort, intPort, testutil.PrivKey1, common.HexToAddress("tmp"), &voting.Config{
		ProposalExpiration: 100 * time.Second,
		MaxPendingRequests: 100,
	})
	ts := runProxyServers(t, cfg)

	defer redisCleanup()

	go teeServer.StartServer()
	setProxyUrl(t, intPort)
	// End of setup

	actionId, err := utils.GenerateRandom()
	require.NoError(t, err)

	teeId, teePubKey := getTeeInfo(t, cfg, actionId)
	require.NotNil(t, teePubKey)
	err = ts.resultService.SetIdentity(teeId)
	require.NoError(t, err)
	logger.Info("Got TEE info")

	cfg.is.SetTeeID(teeId)

	actionId, err = utils.GenerateRandom()
	require.NoError(t, err)

	lastPolicy, providerPrivKeys := initializePolicy(t, cfg, actionId, startingEpochId)
	logger.Info("Initialized policy")

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(int64(lastPolicy.RewardEpochID)),
		StartVotingRoundId: lastPolicy.StartVotingRoundID,
		Threshold:          lastPolicy.Threshold,
		Seed:               lastPolicy.Seed,
		Voters:             lastPolicy.Voters.Voters(),
		Weights:            GetVoterWeights(lastPolicy),
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}
	policy := policy.NewSigningPolicy(&event, nil)

	//Todo: For some reason when I create the round on the cfg.vs it's not mirrored in the external server.
	//TODO: I am passing the reference so it should be but for some reason it isn't.
	cfg.vs.CreateRound(policy)

	var walletId = common.HexToHash("0xabcdef")
	var keyId = uint64(1)
	walletProof := generateWallet(t, cfg, teeId, walletId, keyId, providerPrivKeys, adminWalletPublicKeys, policy.RewardEpochID)
	require.Equal(t, walletProof.Restored, false)
	logger.Info("Created wallet proof")

	paymentHash := "560ccd6e79ba7166e82dbf2a5b9a52283a509b63c39d4a4cc7164db3e43484c4"
	signTransaction(t, cfg, teeId, walletId, keyId, providerPrivKeys, policy.RewardEpochID, paymentHash)
	logger.Info("Signed transaction")

	// nonce := big.NewInt(1)
	actionId, err = utils.GenerateRandom()
	require.NoError(t, err)

	walletBackup := getBackup(t, cfg, teeId, walletId, keyId, actionId)
	_ = walletBackup
	logger.Info("Got backup")

	// Todo: currently wallets are not deleted from the storage after the KeyDelete instruction is sent (must add this)
	// nonce := big.NewInt(1)
	// deleteWallet(t, cfg, teeId, walletId, keyId, providerPrivKeys, uint32(policy.RewardEpochID), nonce)
	// logger.Info("Deleted wallet")

	// TODO Implement Cosigners in meta.go
	/*	recoveredWalletProof := recoverWallet(t, cfg, teeId, teePubKey, walletId, keyId, providerPrivKeys, adminPrivKeys, uint32(policy.RewardEpochID), nonce, walletBackup)
		logger.Info("Recovered wallet")

		walletProof.Restored = true
		_ = recoveredWalletProof

		walletProof.Nonce = nonce
		require.Equal(t, walletProof, recoveredWalletProof)

		getTeeAttestation(t, cfg, teeId, providerPrivKeys, uint32(policy.RewardEpochID))*/
}

func setProxyUrl(t *testing.T, proxyPort int) {
	request := types.ConfigureProxyUrlRequest{
		Url: fmt.Sprintf("http://localhost:%d", proxyPort),
	}

	client := http.Client{
		Timeout: time.Second,
	}
	requestBody, err := json.Marshal(request)
	require.NoError(t, err)

	r, err := client.Post(fmt.Sprintf("http://localhost:%d/configure", 5500), "application/json", bytes.NewBuffer(requestBody))
	require.NoError(t, err)
	require.Equal(t, r.StatusCode, http.StatusOK)
}

func initializePolicy(t *testing.T, pc *proxyConfig, actionId common.Hash, epochId uint32) (*policy.SigningPolicy, []*ecdsa.PrivateKey) {
	t.Helper()

	// Generate random voters and corresponding private keys
	numVoters := 100
	voters, privKeys, pubKeysMap := GenerateRandomKeys(numVoters)
	// Generate a random initial policy
	randSeed := int64(12345)

	initialPolicy := GenerateRandomPolicyData(epochId, voters, randSeed)

	pubKeys := make([]tee.PublicKey, len(voters))
	for i, voter := range voters {
		pubKeys[i] = types.PubKeyToStruct(pubKeysMap[voter])
	}

	req := &types.InitializePolicyRequest{
		InitialPolicyBytes: initialPolicy.RawBytes(),
		PublicKeys:         pubKeys,
	}

	// Note: types.Threshold is used so we can get the result from the external server

	message, err := json.Marshal(req)
	require.NoError(t, err)

	a, err := queue.PrepareDirectAction(constants.Policy, constants.InitializePolicy, message)
	require.NoError(t, err)

	// TODO Implementation of this in proxy server!
	err = pc.aq.Enqueue(t.Context(), a, queue.Main)
	require.NoError(t, err)

	time.Sleep(1000 * time.Millisecond)

	res, err := pc.rs.GetResult(t.Context(), a.Data.ID, types.Submit)
	require.NoError(t, err)

	require.Equal(t, types.Submit, res.Result.SubmissionTag)
	require.True(t, res.Result.Status)

	return initialPolicy, privKeys
}

func getTeeInfo(t *testing.T, pc *proxyConfig, actionId common.Hash) (common.Address, *ecdsa.PublicKey) {
	t.Helper()

	challenge, err := utils.GenerateRandom()
	require.NoError(t, err)
	req := &types.TeeInfoRequest{
		Challenge: common.Hash(challenge),
	}

	message, err := json.Marshal(req)
	require.NoError(t, err)

	a, err := queue.PrepareDirectAction(constants.Get, constants.TEEInfo, message)
	require.NoError(t, err)

	// TODO This is from info.go - check out how to spin up the db!
	err = pc.aq.Enqueue(t.Context(), a, queue.Read)
	require.NoError(t, err)

	// TODO This is ugly - check if we can do a pub/sub on redis / maybe do a exponential backoff for max N ms!
	time.Sleep(500 * time.Millisecond)

	res, err := pc.rs.GetResult(t.Context(), a.Data.ID, types.Submit)
	require.NoError(t, err)

	var teeInfoResponse types.TeeInfoResponse
	err = json.Unmarshal(res.Result.Data, &teeInfoResponse)
	require.NoError(t, err)

	teePubKey, err := types.ParsePubKey(teeInfoResponse.TeeInfo.PublicKey)
	require.NoError(t, err)

	teeId := crypto.PubkeyToAddress(*teePubKey)

	// err = utils.VerifySignature(crypto.Keccak256(res.Data), res.Signature, teeId)
	// require.NoError(t, err)

	// Todo:Verify signature

	return teeId, teePubKey
}

func generateWallet(t *testing.T, pc *proxyConfig, teeId common.Address, walletId [32]byte, keyId uint64, privKeys []*ecdsa.PrivateKey,
	adminWalletPublicKeys []commonwallet.PublicKey, rewardEpochId uint32) *commonwallet.ITeeWalletKeyManagerKeyExistence {
	originalMessage := commonwallet.ITeeWalletKeyManagerKeyGenerate{
		TeeId:    teeId,
		WalletId: walletId,
		KeyId:    keyId,
		OpType:   constants.Wallet.Hash(), // TODO constants.Wallet.Hash() probably
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

	iData, err := BuildInstructionData(constants.Wallet, constants.KeyGenerate, originalMessageEncoded, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	receipts := SignAndSendInstructions(t, iData, privKeys, pc.extPort) //TODO: Verify the receipts are correct for these requests

	VerifyReceipts(t, receipts, iData)

	res := GetActionResponse(t, pc.extPort, iData.InstructionId, testTimeConfig.Timeout, testTimeConfig.Interval)

	require.True(t, res.Result.Status)
	require.Equal(t, types.Threshold, res.Result.SubmissionTag)
	require.Equal(t, constants.Wallet.Hash(), res.Result.OPType)
	require.Equal(t, constants.KeyGenerate.Hash(), res.Result.OPCommand)

	// err = utils.VerifySignature(crypto.Keccak256(res.Data), res.Signature, teeId)
	// require.NoError(t, err)

	require.True(t, res.Result.Status)
	// err = utils.VerifySignature(crypto.Keccak256(res.Data), res.Signature, teeId)
	// require.NoError(t, err)

	walletExistenceProof, err := structs.Decode[commonwallet.ITeeWalletKeyManagerKeyExistence](commonwallet.KeyExistenceStructArg, res.Result.Data)
	require.NoError(t, err)
	_ = walletExistenceProof

	wst := make(chan bool, 1)
	go pc.ws.RunInfo(t.Context(), wst)
	wst <- true

	time.Sleep(2000 * time.Millisecond)

	// err = pc.ws.Sync(t.Context())
	// require.NoError(t, err)

	walletInfo, err := pc.ws.WalletInfo(walletExistenceProof.WalletId)
	require.NoError(t, err)
	require.Equal(t, walletExistenceProof.WalletId, walletInfo.WalletId)
	require.Equal(t, walletExistenceProof.KeyId, walletInfo.KeyId)
	require.Equal(t, walletExistenceProof.AddressStr, walletInfo.AddressStr)

	// todo:  Test for rewards end submissiontag

	return &walletExistenceProof
}

func signTransaction(t *testing.T, pc *proxyConfig, teeId common.Address, walletId [32]byte, keyId uint64, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32, paymentHash string) {
	originalMessage := payment.ITeePaymentsPaymentInstructionMessage{
		WalletId:         walletId,
		TeeIdKeyIdPairs:  []payment.TeeIdKeyIdPair{{TeeId: teeId, KeyId: keyId}},
		SenderAddress:    "rUpy3eEg8rqjqfUoLeBnZkscbKbFsKXC3v",
		RecipientAddress: "rMuZNV2kjCKs8v8rd8QFizAaPdvCDYTPc7",
		Amount:           big.NewInt(1000000000),
		Fee:              big.NewInt(10),
		PaymentReference: [32]byte{},
		Nonce:            0,
		SubNonce:         0,
		BatchEndTs:       0,
	}

	originalMessageEncoded, err := abi.Arguments{payment.MessageArguments[constants.Pay]}.Pack(originalMessage)
	require.NoError(t, err)

	additionalFixedMessage := types.SignPaymentAdditionalFixedMessage{
		PaymentHash: paymentHash,
		KeyId:       keyId,
	}

	iData, err := BuildInstructionData(constants.XRP, constants.Pay, originalMessageEncoded, additionalFixedMessage, teeId, rewardEpochId)
	require.NoError(t, err)

	receipts := SignAndSendInstructions(t, iData, privKeys, pc.extPort)
	VerifyReceipts(t, receipts, iData)

	res := GetActionResponse(t, pc.extPort, iData.InstructionId, testTimeConfig.Timeout, testTimeConfig.Interval)
	require.True(t, res.Result.Status)
	require.Equal(t, types.Threshold, res.Result.SubmissionTag)
	require.Equal(t, constants.XRP.Hash(), res.Result.OPType)
	require.Equal(t, constants.Pay.Hash(), res.Result.OPCommand)

	// err = utils.VerifySignature(crypto.Keccak256(res.Data), res.Signature, teeId)
	// require.NoError(t, err)

	// var signatureData types.GetPaymentSignatureResponse
	// err = json.Unmarshal(res.Data, &signatureData)
	// require.NoError(t, err)

	// TODO: generate action sent when voting closed - how do we wait for end of voting? ig we can hardcode the end of voting, but whether the test will pass depends on machine speed
	/*
		action, err = testutils.BuildMockQueuedActionInstruction(
				"XRP", "PAY", originalMessageEncoded, privKeys, teeId, rewardEpochId, additionalFixedMessage, nil, types.End,
			)
			require.NoError(t, err)

			actionInfo = &types.ActionInfo{QueueId: "main", ActionId: common.BigToHash(actionId)}

			actionMap[*actionInfo] = action
			actionInfoChan <- actionInfo

			actionResponse = <-actionResponseChan
			require.True(t, actionResponse.Status)
			err = utils.VerifySignature(crypto.Keccak256(actionResponse.Result.ResultData.Message), actionResponse.Result.ResultData.Signature, teeId)
			require.NoError(t, err)

			var signerSequence types.SignerSequence
			err = json.Unmarshal(actionResponse.Result.ResultData.Message, &signerSequence)
			require.NoError(t, err)

			err = utils.VerifySignature(signerSequence.Data.VoteHash, signerSequence.Signature, teeId)
			require.NoError(t, err)
	*/
}

func deleteWallet(t *testing.T, pc *proxyConfig, teeId common.Address, walletId [32]byte, keyId uint64,
	privKeys []*ecdsa.PrivateKey, rewardEpochId uint32, nonce *big.Int) {
	originalMessage := commonwallet.ITeeWalletKeyManagerKeyDelete{
		TeeId:    teeId,
		WalletId: walletId,
		KeyId:    keyId,
		Nonce:    nonce,
	}
	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[constants.KeyDelete]}.Pack(originalMessage)
	require.NoError(t, err)

	iData, err := BuildInstructionData(constants.Wallet, constants.KeyDelete, originalMessageEncoded, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	receipts := SignAndSendInstructions(t, iData, privKeys, pc.extPort)
	VerifyReceipts(t, receipts, iData)

	res := GetActionResponse(t, pc.extPort, iData.InstructionId, testTimeConfig.Timeout, testTimeConfig.Interval)
	require.True(t, res.Result.Status)
	require.Equal(t, types.Threshold, res.Result.SubmissionTag)
	require.Equal(t, constants.Wallet.Hash(), res.Result.OPType)
	require.Equal(t, constants.KeyDelete.Hash(), res.Result.OPCommand)

	wst := make(chan bool, 1)
	go pc.ws.RunInfo(t.Context(), wst)
	wst <- true

	time.Sleep(500 * time.Millisecond)

	// // TODO cleaning old storage in walletStorage
	// _, err = pc.ws.WalletInfo(walletId)
	// require.Error(t, err)

	// _, err = pc.ws.KeyInfo(walletId, keyId)
	// require.Error(t, err)

	// TODO
	// generate action sent when voting closed
	/*action, err = testutils.BuildMockQueuedActionInstruction(
		"WALLET", "KEY_DELETE", originalMessageEncoded, privKeys, teeId, rewardEpochId, nil, nil, types.End,
	)
	require.NoError(t, err)

	actionInfo = &types.ActionInfo{QueueId: "main", ActionId: common.BigToHash(actionId)}

	actionMap[*actionInfo] = action
	actionInfoChan <- actionInfo

	actionResponse = <-actionResponseChan
	require.True(t, actionResponse.Status)
	err = utils.VerifySignature(crypto.Keccak256(actionResponse.Result.ResultData.Message), actionResponse.Result.ResultData.Signature, teeId)
	require.NoError(t, err)

	var signerSequence types.SignerSequence
	err = json.Unmarshal(actionResponse.Result.ResultData.Message, &signerSequence)
	require.NoError(t, err)

	err = utils.VerifySignature(signerSequence.Data.VoteHash, signerSequence.Signature, teeId)
	require.NoError(t, err)*/
}

// func recoverWallet(t *testing.T, pc *proxyConfig, teeId common.Address, teePubKey *ecdsa.PublicKey, walletId [32]byte, keyId uint64, providersPrivKeys, adminsPrivKeys []*ecdsa.PrivateKey, rewardEpochId uint32, nonce *big.Int, walletBackup *backup.WalletBackup) *commonwallet.ITeeWalletKeyManagerKeyExistence {
// 	originalMessage := commonwallet.ITeeWalletBackupManagerKeyDataProviderRestore{
// 		TeeId:     teeId,
// 		BackupUrl: "blabla",
// 		Nonce:     nonce,
// 		BackupId: commonwallet.ITeeWalletBackupManagerBackupId{
// 			TeeId:         teeId,
// 			WalletId:      walletId,
// 			KeyId:         keyId,
// 			OpType:        constants.Wallet.Hash(),
// 			PublicKey:     append(walletBackup.PublicKey.X, walletBackup.PublicKey.Y...),
// 			RewardEpochId: big.NewInt(int64(rewardEpochId)),
// 			RandomNonce:   new(big.Int).SetBytes(walletBackup.RandomNonce),
// 		},
// 	}

// 	originalMessageEncoded, err := abi.Arguments{commonwallet.MessageArguments[constants.KeyDataProviderRestore]}.Pack(originalMessage)
// 	require.NoError(t, err)

// 	additionalFixedMessage := walletBackup.WalletBackupMetaData
// 	iData, err := BuildInstructionData(constants.Wallet, constants.KeyDataProviderRestore, originalMessageEncoded, additionalFixedMessage, teeId, rewardEpochId)
// 	require.NoError(t, err)

// 	privKeys := make([]*ecdsa.PrivateKey, 0)
// 	for i, privKey := range providersPrivKeys {
// 		keySplit, err := backup.DecryptSplit(walletBackup.ProviderEncryptedParts.Splits[i], privKey)
// 		require.NoError(t, err)

// 	receipts := SignAndSendInstructions(t, iData, privKeys, pc.extPort)
// 	VerifyReceipts(t, receipts, iData)

// 	res := GetActionResult(t, pc.extPort, iData.InstructionId, testTimeConfig.Timeout*2, testTimeConfig.Interval)
// 	require.True(t, res.Status)
// 	require.Equal(t, types.Threshold, res.SubmissionTag)
// 	require.Equal(t, constants.Wallet.Hash(), res.OPType)
// 	require.Equal(t, constants.KeyDelete.Hash(), res.OPCommand)

// 	//err = utils.VerifySignature(crypto.Keccak256(actionResponse.Data), actionResponse.Signature, teeId)
// 	//require.NoError(t, err)

// 	walletExistenceProof, err := structs.Decode[commonwallet.ITeeWalletKeyManagerKeyExistence](commonwallet.KeyExistenceStructArg, res.Data)
// 	require.NoError(t, err)

// 	wst := make(chan bool, 1)
// 	go pc.ws.RunInfo(t.Context(), wst)
// 	wst <- true

// 	// err = pc.ws.Sync(t.Context())
// 	// require.NoError(t, err)

// 	// check that wallet is actually on the tee
// 	wallet, err := pc.ws.WalletInfo(walletId)
// 	require.NoError(t, err)
// 	require.Equal(t, walletId, wallet.WalletId)
// 	require.Equal(t, keyId, wallet.KeyId)

// 	// TOD:test rewarding dazta

// 	// adminAndProvider := make(map[common.Address]int)
// 	// for j, adminPrivKey := range adminsPrivKeys {
// 	// 	address := crypto.PubkeyToAddress(adminPrivKey.PublicKey)
// 	// 	for _, providerPrivKey := range providersPrivKeys {
// 	// 		if address == crypto.PubkeyToAddress(providerPrivKey.PublicKey) {
// 	// 			adminAndProvider[address] = j
// 	// 		}
// 	// 	}
// 	// }

// 	// teeEciesPubKey := ecies.ImportECDSAPublic(teePubKey)
// 	// additionalVariableMessages := make([]interface{}, 0)

// 	// 	address := crypto.PubkeyToAddress(privKey.PublicKey)
// 	// 	j, check := adminAndProvider[address]
// 	// 	var plaintext []byte
// 	// 	if !check {
// 	// 		plaintext, err = json.Marshal(keySplit)
// 	// 		require.NoError(t, err)
// 	// 	} else {
// 	// 		keySplitAdmin, err := backup.DecryptSplit(walletBackup.AdminEncryptedParts.Splits[j], privKey)
// 	// 		require.NoError(t, err)
// 	// 		var twoKeySplits [2]backup.KeySplit
// 	// 		twoKeySplits[0] = *keySplit
// 	// 		twoKeySplits[1] = *keySplitAdmin
// 	// 		plaintext, err = json.Marshal(twoKeySplits)
// 	// 		require.NoError(t, err)
// 	// 	}

// 	// 	cipher, err := ecies.Encrypt(rand.Reader, teeEciesPubKey, plaintext, nil, nil)
// 	// 	require.NoError(t, err)

// 	// 	additionalVariableMessages = append(additionalVariableMessages, cipher)
// 	// 	privKeys = append(privKeys, privKey)
// 	// }

// 	// for i, privKey := range adminsPrivKeys {
// 	// 	address := crypto.PubkeyToAddress(privKey.PublicKey)
// 	// 	_, check := adminAndProvider[address]
// 	// 	if check {
// 	// 		continue
// 	// 	}

// 	// 	keySplit, err := backup.DecryptSplit(walletBackup.AdminEncryptedParts.Splits[i], privKey)
// 	// 	require.NoError(t, err)

// 	// 	plaintext, err := json.Marshal(keySplit)
// 	// 	require.NoError(t, err)

// 	// 	cipher, err := ecies.Encrypt(rand.Reader, teeEciesPubKey, plaintext, nil, nil)
// 	// 	require.NoError(t, err)

// 	// 	additionalVariableMessages = append(additionalVariableMessages, cipher)
// 	// 	privKeys = append(privKeys, privKey)
// 	// }

// 	/*// generate action sent when voting closed
// 	action, err = testutils.BuildMockQueuedActionInstruction(
// 		"WALLET", "KEY_DATA_PROVIDER_RESTORE", originalMessageEncoded, privKeys, teeId,
// 		rewardEpochId, additionalFixedMessage, additionalVariableMessages,
// 		types.End,
// 	)
// 	require.NoError(t, err)

// 	actionInfo = &types.ActionInfo{QueueId: "main", ActionId: common.BigToHash(actionId)}

// 	actionMap[*actionInfo] = action
// 	actionInfoChan <- actionInfo

// 	actionResponse = <-actionResponseChan
// 	require.True(t, actionResponse.Status)
// 	err = utils.VerifySignature(crypto.Keccak256(actionResponse.Result.ResultData.Message), actionResponse.Result.ResultData.Signature, teeId)
// 	require.NoError(t, err)

// 	var signerSequence types.SignerSequence
// 	err = json.Unmarshal(actionResponse.Result.ResultData.Message, &signerSequence)
// 	require.NoError(t, err)

// 	err = utils.VerifySignature(signerSequence.Data.VoteHash, signerSequence.Signature, teeId)
// 	require.NoError(t, err)

// 	return &walletExistenceProof*/

// 	return &walletExistenceProof
// }

func getTeeAttestation(t *testing.T, pc *proxyConfig, teeId common.Address, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32) {

	challenge, err := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil))
	require.NoError(t, err)

	originalMessage := verification.ITeeVerificationTeeAttestation{
		TeeMachine: verification.ITeeRegistryTeeMachineWithAttestationData{
			TeeId:        teeId,
			InitialTeeId: common.Address{},
			Url:          "bla",
			CodeHash:     [32]byte{},
			Platform:     [32]byte{},
		},
		Challenge: [32]byte(challenge.Bytes()),
	}
	originalMessageEncoded, err := abi.Arguments{verification.MessageArguments[constants.TEEAttestation]}.Pack(originalMessage)
	require.NoError(t, err)

	iData, err := BuildInstructionData(constants.Reg, constants.TEEAttestation, originalMessageEncoded, nil, teeId, rewardEpochId)
	require.NoError(t, err)

	receipts := SignAndSendInstructions(t, iData, privKeys, pc.extPort)
	VerifyReceipts(t, receipts, iData)

	actionResponse := GetActionResponse(t, pc.extPort, iData.InstructionId, testTimeConfig.Timeout, testTimeConfig.Interval)
	require.True(t, actionResponse.Result.Status)
	require.Equal(t, types.Threshold, actionResponse.Result.SubmissionTag)
	require.Equal(t, constants.Wallet.Hash(), actionResponse.Result.OPType)
	require.Equal(t, constants.KeyDelete.Hash(), actionResponse.Result.OPCommand)

	//err = utils.VerifySignature(crypto.Keccak256(actionResponse.Data), actionResponse.Signature, teeId)
	//require.NoError(t, err)

	//var teeInfoResponse types.TeeInfoResponse
	//err = json.Unmarshal(actionResponse.Data, &teeInfoResponse)
	//require.NoError(t, err)
	//
	//teePubKey, err := types.ParsePubKey(teeInfoResponse.Attestation.PublicKey)
	//require.NoError(t, err)
	//
	//receivedTeeId := crypto.PubkeyToAddress(*teePubKey)
	//
	//require.Equal(t, receivedTeeId, teeId)

	// TODO
	// generate action sent when voting closed
	/*action, err = testutils.BuildMockQueuedActionInstruction(
		"REG", "TEE_ATTESTATION", originalMessageEncoded, privKeys, teeId, rewardEpochId, nil, nil, types.End,
	)
	require.NoError(t, err)

	actionInfo = &types.ActionInfo{QueueId: "main", ActionId: common.BigToHash(actionId)}

	actionMap[*actionInfo] = action
	actionInfoChan <- actionInfo

	actionResponse = <-actionResponseChan
	require.True(t, actionResponse.Status)
	err = utils.VerifySignature(crypto.Keccak256(actionResponse.Result.ResultData.Message), actionResponse.Result.ResultData.Signature, teeId)
	require.NoError(t, err)

	var signerSequence types.SignerSequence
	err = json.Unmarshal(actionResponse.Result.ResultData.Message, &signerSequence)
	require.NoError(t, err)

	err = utils.VerifySignature(signerSequence.Data.VoteHash, signerSequence.Signature, teeId)
	require.NoError(t, err)*/
}

// TODO Probably should be a part of common
type WalletKeyIdPair struct {
	WalletId common.Hash
	KeyId    uint64
}

func getBackup(t *testing.T, pc *proxyConfig, teeId common.Address, walletId [32]byte, keyId uint64, actionId common.Hash) *backup.WalletBackup {
	message := &WalletKeyIdPair{
		WalletId: walletId,
		KeyId:    keyId,
	}

	msg, err := json.Marshal(message)
	require.NoError(t, err)

	a, err := queue.PrepareDirectAction(constants.Get, constants.TEEBackup, msg)
	require.NoError(t, err)

	err = pc.aq.Enqueue(t.Context(), a, queue.Main)
	require.NoError(t, err)

	time.Sleep(1000 * time.Millisecond)

	res, err := pc.rs.GetResult(t.Context(), a.Data.ID, types.Submit)
	require.NoError(t, err)

	//err = utils.VerifySignature(crypto.Keccak256(res.Data), res.Signature, teeId)
	//require.NoError(t, err)

	var backupResponse types.WalletGetBackupResponse
	err = json.Unmarshal(res.Result.Data, &backupResponse)
	require.NoError(t, err)

	//fmt.Println("backup size", len(backupResponse.WalletBackup))

	var backup backup.WalletBackup
	err = json.Unmarshal(backupResponse.WalletBackup, &backup)
	require.NoError(t, err)

	return &backup
}

// WriteProxyConfigJSON writes a config.Proxy struct to ./config.json as JSON.
func WriteProxyConfigJSON(t *testing.T, proxy config.Proxy) string {
	b, err := json.MarshalIndent(proxy, "", "  ")
	require.NoError(t, err)
	path := "./config.json"
	err = os.WriteFile(path, b, 0644)
	require.NoError(t, err)
	return path
}
