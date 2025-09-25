package integration

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"

	"github.com/ethereum/go-ethereum/crypto"

	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"github.com/flare-foundation/go-flare-common/pkg/policy"

	"github.com/flare-foundation/tee-proxy/internal/testutil"

	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payment"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"

	intactions "github.com/flare-foundation/tee-proxy/integration/actions"
	integrationUtils "github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/flare-foundation/tee-proxy/integration/xrpl"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	xrplclient "github.com/xrpscan/xrpl-go"
)

func TestXRPIntegration(t *testing.T) {
	t.Skip("Skipping XRP integration test")

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

	go teeServer.StartServerPMW(intPort)
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyUrlOnTee(t, teePort, proxyUrl)

	var wgProxy sync.WaitGroup
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1, &wgProxy)
	// End of setup

	lastPolicy, _, providerPrivKeys, _ := intactions.InitializePolicy(t, cfg, startingEpochId)
	logger.Info("Initialized policy")

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(int64(lastPolicy.RewardEpochID)),
		StartVotingRoundId: lastPolicy.StartVotingRoundID,
		Threshold:          lastPolicy.Threshold,
		Seed:               lastPolicy.Seed,
		Voters:             lastPolicy.Voters.Voters(),
		Weights:            integrationUtils.GetVoterWeights(lastPolicy),
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}
	policy := policy.NewSigningPolicy(&event, nil)
	cfg.Vs.CreateRound(policy)

	walletIds := make([]common.Hash, 3)
	walletAddresses := make([]string, 3)
	keyId := uint64(1)
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			walletIds[i] = common.HexToHash(fmt.Sprintf("0x%x", i))

			walletProof := intactions.GenerateWallet(t, cfg, cfg.TeeID, walletIds[i], keyId, providerPrivKeys, adminWalletPublicKeys, policy.RewardEpochID)
			require.False(t, walletProof.Restored, "getting wallet response")
		}(i)
	}
	wg.Wait()

	multisigResult := integrationUtils.CreateMultisigWallet(t, "./scripts", walletAddresses, 2)
	require.True(t, multisigResult.Success, "creating multisig wallet")
	require.Greater(t, multisigResult.Balance, 0, "multisig wallet balance should be greater than 0")
	logger.Info("Created multisig wallet: %s with balance: %d", multisigResult.MultisigAddress, multisigResult.Balance)

	sequence, lastLedgerSequence, err := xrpl.GetTransactionParams(multisigResult.MultisigAddress, 10)
	require.NoError(t, err)
	logger.Info("Sequence: %d, Last ledger sequence: %d", sequence, lastLedgerSequence)

	paymentInstruction := payment.ITeePaymentsPaymentInstructionMessage{
		WalletId:         [32]byte{}, // add wallet id in loop
		TeeIdKeyIdPairs:  []payment.TeeIdKeyIdPair{{TeeId: cfg.TeeID, KeyId: keyId}},
		SenderAddress:    multisigResult.MultisigAddress,
		RecipientAddress: "rJQesZZEQzW9J3Eb1X1Snc7E6YGk7kTMoK",
		Amount:           big.NewInt(1_000_000),
		Fee:              big.NewInt(50),
		PaymentReference: [32]byte{},
		Nonce:            uint64(sequence),
		SubNonce:         0,
		BatchEndTs:       0,
	}

	var tx map[string]any
	var txHash []byte
	signers := make([]intactions.SignerData, 0)
	for i := range 3 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			paymentInstruction.WalletId = walletIds[i]

			txData := intactions.SignTransaction(t, cfg, cfg.TeeID, paymentInstruction, providerPrivKeys, policy.RewardEpochID)
			signers = append(signers, txData.Signers...)
			tx = txData.Tx
			txHash = txData.TxHash
		}(i)
	}
	wg.Wait()

	logger.Info("Signed transaction: %v", txHash)

	client := xrplclient.NewClient(xrplclient.ClientConfig{
		URL: "wss://s.altnet.rippletest.net:51233", // TODO: Make this configurable
	})

	txRes, err := xrpl.SubmitMultisignedTx(client, tx, signers)
	require.NoError(t, err)

	logger.Info("Submitted transaction: %v", txRes.TransactionHash)

	cleanup()
	wgProxy.Wait()
}
