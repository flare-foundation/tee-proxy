package actions

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeUtils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/flare-foundation/tee-node/pkg/wallets/backup"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/test/integration/utils"
	testutils "github.com/flare-foundation/tee-proxy/test/utils"
	"github.com/stretchr/testify/require"
)

func InitializePolicy(t *testing.T, pc *utils.ProxyConfig, epochId uint32) (*policy.SigningPolicy, []common.Address, []*ecdsa.PrivateKey, map[common.Address]*ecdsa.PublicKey) {
	t.Helper()

	// Generate random voters and corresponding private keys
	numVoters := 100
	voters, privKeys, pubKeysMap := testutils.GenerateRandomKeys(numVoters)
	// Generate a random initial policy
	seed := int64(12345)

	initialPolicy := testutils.GenerateRandomPolicyData(epochId, voters, seed)

	pubKeys := make([]types.PublicKey, len(voters))
	for i, voter := range voters {
		pubKeys[i] = types.PubKeyToStruct(pubKeysMap[voter])
	}

	req := &types.InitializePolicyRequest{
		InitialPolicyBytes: initialPolicy.RawBytes(),
		PublicKeys:         pubKeys,
	}

	message, err := json.Marshal(req)
	require.NoError(t, err)

	a, err := queue.PrepareDirectAction(op.Policy, op.InitializePolicy, message)
	require.NoError(t, err)

	err = pc.Aq.Enqueue(t.Context(), a, processorutils.Direct)
	require.NoError(t, err)

	res, err := pc.Rs.WaitOnResponse(t.Context(), a.Data.ID, types.Submit, utils.TestTimeConfig.Timeout)
	require.NoError(t, err)

	require.Equal(t, types.Submit, res.Result.SubmissionTag)
	require.Equal(t, uint8(1), res.Result.Status)

	return initialPolicy, voters, privKeys, pubKeysMap
}

func UpdatePolicy(t *testing.T, pc *utils.ProxyConfig, epochID uint32, voters []common.Address, privKeys []*ecdsa.PrivateKey, pubKeysMap map[common.Address]*ecdsa.PublicKey) (*policy.SigningPolicy, []common.Address, []*ecdsa.PrivateKey, map[common.Address]*ecdsa.PublicKey) {
	t.Helper()

	randSeed := int64(12345)
	nextPolicy := testutils.GenerateRandomPolicyData(epochID, voters, randSeed)

	policySignatures := testutils.BuildMultiSignedPolicy(nextPolicy.RawBytes(), privKeys)

	pubKeys := make([]types.PublicKey, len(voters))
	for i, voter := range voters {
		pubKeys[i] = types.PubKeyToStruct(pubKeysMap[voter])
	}

	updatePolicyRequest := types.UpdatePolicyRequest{
		NewPolicy:  policySignatures,
		PublicKeys: pubKeys,
	}
	updatePolicyRequestBytes, err := json.Marshal(updatePolicyRequest)
	require.NoError(t, err)

	a, err := queue.PrepareDirectAction(op.Policy, op.UpdatePolicy, updatePolicyRequestBytes)
	require.NoError(t, err)

	err = pc.Aq.Enqueue(t.Context(), a, processorutils.Direct)
	require.NoError(t, err)

	res, err := pc.Rs.WaitOnResponse(t.Context(), a.Data.ID, types.Submit, utils.TestTimeConfig.Timeout)
	require.NoError(t, err)

	require.Equal(t, types.Submit, res.Result.SubmissionTag)
	require.Equal(t, uint8(1), res.Result.Status)

	return nextPolicy, voters, privKeys, pubKeysMap
}

func GetBackup(t *testing.T, pc *utils.ProxyConfig, walletID [32]byte, keyID uint64, teeID common.Address) *backup.WalletBackup {
	t.Helper()

	message := &wallets.KeyIDPair{
		WalletID: walletID,
		KeyID:    keyID,
	}

	msg, err := json.Marshal(message)
	require.NoError(t, err)

	a, err := queue.PrepareDirectAction(op.Get, op.TEEBackup, msg)
	require.NoError(t, err)

	err = pc.Aq.Enqueue(t.Context(), a, processorutils.Direct)
	require.NoError(t, err)

	res, err := pc.Rs.WaitOnResponse(t.Context(), a.Data.ID, types.Submit, utils.TestTimeConfig.Timeout)
	require.NoError(t, err)

	err = teeUtils.VerifySignature(utils.ActionResultSignHash(t, res.Result.Hash()), res.Signature, teeID)
	require.NoError(t, err)

	var backupResponse wallets.TEEBackupResponse
	err = json.Unmarshal(res.Result.Data, &backupResponse)
	require.NoError(t, err)

	var backup backup.WalletBackup
	err = json.Unmarshal(backupResponse.WalletBackup, &backup)
	require.NoError(t, err)

	return &backup
}
