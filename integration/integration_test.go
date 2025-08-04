package integration

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"

	"github.com/ethereum/go-ethereum/crypto"

	teeServer "github.com/flare-foundation/tee-node/pkg/server"

	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"github.com/flare-foundation/go-flare-common/pkg/policy"

	"github.com/flare-foundation/tee-proxy/internal/testutil"

	"github.com/stretchr/testify/require"

	commonwallet "github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"

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
	adminWalletPublicKeys := make([]commonwallet.PublicKey, len(adminPubKeys))
	for i, pubKey := range adminPubKeys {
		adminWalletPublicKeys[i] = commonwallet.PublicKey(types.PubKeyToStruct(pubKey))
	}

	go teeServer.StartServer()
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyUrlOnTee(t, teePort, proxyUrl)
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1)
	defer cleanup()
	// End of setup

	lastPolicy, providerPrivKeys := intactions.InitializePolicy(t, cfg, startingEpochId)
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

	var walletId = common.HexToHash("0xabcdef")
	var keyId = uint64(1)
	walletProof := intactions.GenerateWallet(t, cfg, cfg.TeeId, walletId, keyId, providerPrivKeys, adminWalletPublicKeys, policy.RewardEpochID)
	require.False(t, walletProof.Restored, "getting wallet response")
	logger.Info("Created wallet proof")

	intactions.SignTransaction(t, cfg, cfg.TeeId, walletId, keyId, providerPrivKeys, policy.RewardEpochID)
	logger.Info("Signed transaction")

	require.NoError(t, err)

	walletBackup := intactions.GetBackup(t, cfg, walletId, keyId, cfg.TeeId)
	_ = walletBackup
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
}
