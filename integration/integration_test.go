package integration

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"github.com/flare-foundation/tee-proxy/internal/testutil"

	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payment"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"

	intactions "github.com/flare-foundation/tee-proxy/integration/actions"
	integrationUtils "github.com/flare-foundation/tee-proxy/integration/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
)

func TestProxyTeeIntegration(t *testing.T) {
	// Start of setup
	const extPort = 8000
	const intPort = 8008
	const teePort = 5500

	numVoters, _, startingEpochId := 100, 10, uint32(1)
	integrationUtils.GenerateRandomKeys(numVoters)

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
	adminWalletPublicKeys := make([]wallet.PublicKey, len(adminPubKeys))
	for i, pubKey := range adminPubKeys {
		pk := types.PubKeyToStruct(pubKey)
		adminWalletPublicKeys[i] = wallet.PublicKey{
			X: pk.X,
			Y: pk.Y,
		}
	}

	go teeServer.StartServer()
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyUrlOnTee(t, teePort, proxyUrl)

	var wgProxy sync.WaitGroup
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1, &wgProxy)
	// End of setup

	policy, voters, providerPrivKeys, providerPubKeysMap := intactions.InitializePolicy(t, cfg, startingEpochId)
	ok := integrationUtils.WaitFor(t, 100*time.Millisecond, 5*time.Second, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return common.BytesToHash(teeInfo.TeeInfo.LastSigningPolicyHash[:]) == common.BytesToHash(policy.Hash()[:])
	})
	require.True(t, ok, "Policy not initialized on TEE")
	logger.Info("Initialized policy")

	cfg.Vs.CreateRound(policy)

	var walletId = common.HexToHash("0xabcdef")
	var keyId = uint64(1)
	walletProof := intactions.GenerateWallet(t, cfg, cfg.TeeId, walletId, keyId, providerPrivKeys, adminWalletPublicKeys, policy.RewardEpochID)
	require.False(t, walletProof.Restored, "getting wallet response")
	logger.Info("Created wallet proof")

	paymentInstruction := payment.ITeePaymentsPaymentInstructionMessage{
		WalletId:         walletId,
		TeeIdKeyIdPairs:  []payment.TeeIdKeyIdPair{{TeeId: cfg.TeeId, KeyId: keyId}},
		SenderAddress:    "rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G",
		RecipientAddress: "rJQesZZEQzW9J3Eb1X1Snc7E6YGk7kTMoK",
		Amount:           big.NewInt(1000000000),
		Fee:              big.NewInt(10),
		PaymentReference: [32]byte{},
		Nonce:            0,
		SubNonce:         0,
		BatchEndTs:       0,
	}
	intactions.SignTransaction(t, cfg, cfg.TeeId, paymentInstruction, providerPrivKeys, policy.RewardEpochID)
	logger.Info("Signed transaction")

	walletBackup := intactions.GetBackup(t, cfg, walletId, keyId, cfg.TeeId)
	logger.Info("Got backup")

	nonce := big.NewInt(1)
	intactions.DeleteWallet(t, cfg, walletId, keyId, providerPrivKeys, policy.RewardEpochID, nonce)
	nonce.Add(nonce, common.Big1)
	logger.Info("Deleted wallet")

	recoveredWalletProof := intactions.RecoverWallet(t, cfg, walletId, keyId, providerPrivKeys, adminPrivKeys, policy.RewardEpochID, nonce, walletBackup)
	logger.Info("Recovered wallet")
	walletProof.Restored = true

	walletProof.Nonce = nonce
	require.Equal(t, walletProof, recoveredWalletProof)

	intactions.GetTeeAttestation(t, cfg, providerPrivKeys, policy.RewardEpochID)

	ftdcResponse := intactions.FtdcProve(t, cfg, providerPrivKeys, adminPrivKeys, policy.RewardEpochID)
	require.NotNil(t, ftdcResponse)
	logger.Info("FTDC proof completed")

	startingEpochId++
	newPolicy, _, _, _ := intactions.UpdatePolicy(t, cfg, startingEpochId, voters, providerPrivKeys, providerPubKeysMap)
	logger.Info("Updated policy")

	ok = integrationUtils.WaitFor(t, 100*time.Millisecond, 5*time.Second, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return common.BytesToHash(teeInfo.TeeInfo.LastSigningPolicyHash[:]) == common.BytesToHash(newPolicy.Hash()[:])
	})
	require.True(t, ok, "TEE info did not update to new policy hash in time")

	cleanup()
	wgProxy.Wait()
}
