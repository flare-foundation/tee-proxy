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

	integrationactions "github.com/flare-foundation/tee-proxy/test/integration/actions"
	integrationUtils "github.com/flare-foundation/tee-proxy/test/integration/utils"
	testUtils "github.com/flare-foundation/tee-proxy/test/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
)

func TestProxyTeeIntegration(t *testing.T) {
	// Start of setup
	const extPort = 8000
	const intPort = 8008
	const teePort = 5500

	numVoters, _, startingEpochID := 100, 10, uint32(1)
	testUtils.GenerateRandomKeys(numVoters)

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

	go teeServer.StartServerPMW(teePort)
	time.Sleep(time.Second)
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyURLOnTEE(t, teePort, proxyUrl)

	var wgProxy sync.WaitGroup
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1, &wgProxy)
	// End of setup

	policy, voters, providerPrivKeys, providerPubKeysMap := integrationactions.InitializePolicy(t, cfg, startingEpochID)
	ok := integrationUtils.WaitFor(t, 100*time.Millisecond, 5*time.Second, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return teeInfo.TeeInfo.LastSigningPolicyHash == common.BytesToHash(policy.Hash())
	})
	require.True(t, ok, "Policy not initialized on TEE")
	logger.Info("Initialized policy")

	cfg.Pc <- *policy

	var walletID = common.HexToHash("0xabcdef")
	var keyID = uint64(1)
	walletProof := integrationactions.GenerateWallet(t, cfg, cfg.TeeID, walletID, keyID, providerPrivKeys, adminWalletPublicKeys, policy.RewardEpochID)
	require.False(t, walletProof.Restored, "getting wallet response")
	logger.Info("Created wallet proof")

	paymentInstruction := payment.ITeePaymentsPaymentInstructionMessage{
		WalletId:         walletID,
		TeeIdKeyIdPairs:  []payment.TeeIdKeyIdPair{{TeeId: cfg.TeeID, KeyId: keyID}},
		SenderAddress:    "rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G",
		RecipientAddress: "rJQesZZEQzW9J3Eb1X1Snc7E6YGk7kTMoK",
		Amount:           big.NewInt(1000000000),
		MaxFee:           big.NewInt(10),
		FeeSchedule:      []byte{0x13, 0x88, 0x00, 0x01, 0x27, 0x10, 0x00, 0x02},
		PaymentReference: [32]byte{},
		Nonce:            0,
		SubNonce:         0,
		BatchEndTs:       0,
	}
	integrationactions.SignTransaction(t, cfg, cfg.TeeID, paymentInstruction, providerPrivKeys, policy.RewardEpochID)
	logger.Info("Signed transaction")

	walletBackup := integrationactions.GetBackup(t, cfg, walletID, keyID, cfg.TeeID)
	logger.Info("Got backup")

	nonce := big.NewInt(1)
	integrationactions.DeleteWallet(t, cfg, walletID, keyID, providerPrivKeys, policy.RewardEpochID, nonce)
	nonce.Add(nonce, common.Big1)
	logger.Info("Deleted wallet")

	recoveredWalletProof := integrationactions.RecoverWallet(t, cfg, walletID, keyID, providerPrivKeys, adminPrivKeys, policy.RewardEpochID, nonce, walletBackup)
	logger.Info("Recovered wallet")
	walletProof.Restored = true

	walletProof.Nonce = nonce
	require.Equal(t, walletProof, recoveredWalletProof)

	integrationactions.GetTeeAttestation(t, cfg, providerPrivKeys, policy.RewardEpochID)

	ftdcResponse := integrationactions.FTDCProve(t, cfg, providerPrivKeys, adminPrivKeys, policy.RewardEpochID)
	require.NotNil(t, ftdcResponse)
	logger.Info("FTDC proof completed")

	startingEpochID++
	newPolicy, _, _, _ := integrationactions.UpdatePolicy(t, cfg, startingEpochID, voters, providerPrivKeys, providerPubKeysMap)
	logger.Info("Updated policy")

	ok = integrationUtils.WaitFor(t, 100*time.Millisecond, 5*time.Second, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return teeInfo.TeeInfo.LastSigningPolicyHash == common.BytesToHash(newPolicy.Hash())
	})
	require.True(t, ok, "TEE info did not update to new policy hash in time")

	cleanup()
	wgProxy.Wait()
}
