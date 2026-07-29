package integration

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"github.com/flare-foundation/tee-proxy/internal/testutil"

	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payments"
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
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", teePort), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close() //nolint:errcheck // best-effort cleanup
		return true
	}, 5*time.Second, 50*time.Millisecond, "TEE server did not start listening within timeout")
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyURLOnTEE(t, teePort, proxyUrl)
	integrationUtils.SetChainIDOnTEE(t, teePort, integrationUtils.TestChainID)

	var wgProxy sync.WaitGroup
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1, &wgProxy)
	// End of setup

	policy, voters, providerPrivKeys, providerPubKeysMap := integrationactions.InitializePolicy(t, cfg, startingEpochID)
	require.Eventually(t, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return teeInfo.TeeInfo.LastSigningPolicyHash == common.BytesToHash(policy.Hash())
	}, 5*time.Second, 100*time.Millisecond, "Policy not initialized on TEE")
	t.Log("Initialized policy")

	cfg.Pc <- *policy

	var walletID = common.HexToHash("0xabcdef")
	var keyID = uint64(1)
	walletProof := integrationactions.GenerateWallet(t, cfg, cfg.TeeID, walletID, keyID, providerPrivKeys, adminWalletPublicKeys, policy.RewardEpochID)
	require.False(t, walletProof.Restored, "getting wallet response")
	t.Log("Created wallet proof")

	paymentInstruction := payments.ITeePaymentsPaymentInstructionMessage{
		WalletId:         walletID,
		TeeIdKeyIdPairs:  []payments.TeeIdKeyIdPair{{TeeId: cfg.TeeID, KeyId: keyID}},
		SenderAddress:    "rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G",
		RecipientAddress: "rJQesZZEQzW9J3Eb1X1Snc7E6YGk7kTMoK",
		Amount:           big.NewInt(1000000000),
		MaxFee:           big.NewInt(10),
		FeeSchedule:      []byte{0x13, 0x88, 0x00, 0x01, 0x27, 0x10, 0x00, 0x02},
		PaymentReference: [32]byte{},
		Nonce:            0,
		PaymentId:        0,
	}
	integrationactions.SignTransaction(t, cfg, cfg.TeeID, paymentInstruction, providerPrivKeys, policy.RewardEpochID)
	t.Log("Signed transaction")

	walletBackup := integrationactions.GetBackup(t, cfg, walletID, keyID, cfg.TeeID)
	t.Log("Got backup")

	nonce := big.NewInt(1)
	integrationactions.DeleteWallet(t, cfg, walletID, keyID, providerPrivKeys, policy.RewardEpochID, nonce)
	nonce.Add(nonce, common.Big1)
	t.Log("Deleted wallet")

	recoveredWalletProof := integrationactions.RecoverWallet(t, cfg, walletID, keyID, providerPrivKeys, adminPrivKeys, policy.RewardEpochID, nonce, walletBackup)
	t.Log("Recovered wallet")
	walletProof.Restored = true

	walletProof.Nonce = nonce
	require.Equal(t, walletProof, recoveredWalletProof)

	integrationactions.GetTeeAttestation(t, cfg, providerPrivKeys, policy.RewardEpochID)

	fdcResponse := integrationactions.FDCProve(t, cfg, providerPrivKeys, adminPrivKeys, policy.RewardEpochID)
	require.NotNil(t, fdcResponse)
	t.Log("FDC proof completed")

	startingEpochID++
	newPolicy, _, _, _ := integrationactions.UpdatePolicy(t, cfg, startingEpochID, voters, providerPrivKeys, providerPubKeysMap)
	t.Log("Updated policy")

	require.Eventually(t, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return teeInfo.TeeInfo.LastSigningPolicyHash == common.BytesToHash(newPolicy.Hash())
	}, 5*time.Second, 100*time.Millisecond, "TEE info did not update to new policy hash in time")

	cleanup()
	wgProxy.Wait()
}
