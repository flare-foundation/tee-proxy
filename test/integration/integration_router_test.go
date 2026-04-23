package integration

import (
	"crypto/ecdsa"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	teeServer "github.com/flare-foundation/tee-node/pkg/server"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	intactions "github.com/flare-foundation/tee-proxy/test/integration/actions"
	integrationUtils "github.com/flare-foundation/tee-proxy/test/integration/utils"
	testutils "github.com/flare-foundation/tee-proxy/test/utils"
	"github.com/stretchr/testify/require"
)

func TestProxyTeeIntegration2(t *testing.T) {
	t.Skip() // Note: go test ./... fails with an address(port) already in use error

	// Start of setup
	const extPort = 8000
	const intPort = 8008
	const teePort = 5500
	const extensionPort = 4400
	const extensionServerPort = 4401

	numVoters, _, startingEpochID := 100, 10, uint32(1)
	testutils.GenerateRandomKeys(numVoters)

	go teeServer.StartExampleExtension(extensionServerPort, extensionPort)
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyURLOnTEE(t, teePort, proxyUrl)

	var wgProxy sync.WaitGroup
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1, &wgProxy)

	policy, voters, providerPrivKeys, providerPubKeysMap := intactions.InitializePolicy(t, cfg, startingEpochID)
	require.Eventually(t, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return teeInfo.TeeInfo.LastSigningPolicyHash == common.BytesToHash(policy.Hash())
	}, 5*time.Second, 100*time.Millisecond, "Policy not initialized on TEE")
	t.Log("Initialized policy")

	cfg.Pc <- *policy

	_ = voters
	_ = providerPrivKeys
	_ = providerPubKeysMap

	SendCustomInstruction(t, cfg, providerPrivKeys, startingEpochID)

	cleanup()
}

const MyOp op.Type = "MyOp"
const MyCommand op.Command = "MyCommand"

func SendCustomInstruction(t *testing.T, pc *integrationUtils.ProxyConfig, privKeys []*ecdsa.PrivateKey, rewardEpochID uint32) {
	t.Helper()

	timestamp := uint64(time.Now().Unix())
	iData := integrationUtils.BuildInstructionData(t, MyOp, MyCommand, []byte("asdfasdf"), timestamp, nil, nil, nil, 0, pc.TeeID, rewardEpochID)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := integrationUtils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)

	integrationUtils.VerifyReceipts(t, receipts, iData)

	res := integrationUtils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, MyOp, MyCommand, pc.TeeID, 1)
	require.Equal(t, "successfully posted to extension", string(res.Result.Data))

	require.Eventually(t, func() bool {
		res = integrationUtils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, MyOp, MyCommand, pc.TeeID, 1)
		return res.Result.Log == "Action (type: instruction) processed successfully"
	}, 2*time.Second, 50*time.Millisecond, "action was not processed by extension within timeout")
}
