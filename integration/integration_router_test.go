package integration

import (
	"crypto/ecdsa"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	teeServer "github.com/flare-foundation/tee-node/pkg/server"
	"github.com/flare-foundation/tee-node/pkg/types"
	intactions "github.com/flare-foundation/tee-proxy/integration/actions"
	integrationUtils "github.com/flare-foundation/tee-proxy/integration/utils"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
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

	numVoters, _, startingEpochId := 100, 10, uint32(1)
	integrationUtils.GenerateRandomKeys(numVoters)

	go teeServer.StartExampleExtension(extensionServerPort, extensionPort)
	proxyUrl := fmt.Sprintf("http://localhost:%d", intPort)
	integrationUtils.SetProxyUrlOnTee(t, teePort, proxyUrl)

	var wgProxy sync.WaitGroup
	cfg, cleanup := integrationUtils.RunProxy(t, intPort, extPort, testutil.PrivKey1, &wgProxy)

	policy, voters, providerPrivKeys, providerPubKeysMap := intactions.InitializePolicy(t, cfg, startingEpochId)
	ok := integrationUtils.WaitFor(t, 100*time.Millisecond, 5*time.Second, func() bool {
		teeInfo := integrationUtils.GetTeeInfo(t, cfg)
		return teeInfo.TeeInfo.LastSigningPolicyHash == common.BytesToHash(policy.Hash())
	})

	require.True(t, ok, "Policy not initialized on TEE")
	logger.Info("Initialized policy")

	cfg.Vs.CreateRound(policy)

	_ = voters
	_ = providerPrivKeys
	_ = providerPubKeysMap

	SendCustomInstruction(t, cfg, providerPrivKeys, startingEpochId)

	require.True(t, ok)
	cleanup()
}

const MyOp op.Type = "MyOp"
const MyCommand op.Command = "MyCommand"

func SendCustomInstruction(t *testing.T, pc *integrationUtils.ProxyConfig, privKeys []*ecdsa.PrivateKey, rewardEpochId uint32) {
	timestamp := uint64(time.Now().Unix())
	iData := integrationUtils.BuildInstructionData(t, MyOp, MyCommand, []byte("asdfasdf"), timestamp, nil, nil, nil, 0, pc.TeeID, rewardEpochId)

	endOfVotingTicker := time.NewTicker(pc.Vc.ProposalExpiration)
	defer endOfVotingTicker.Stop()
	receipts := integrationUtils.SignAndSendInstructions(t, iData, privKeys, pc.ExtPort)

	integrationUtils.VerifyReceipts(t, receipts, iData)

	res := integrationUtils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, MyOp, MyCommand, pc.TeeID)
	require.Equal(t, "successfully posted to extension", string(res.Result.Data))

	time.Sleep(1 * time.Second)
	res = integrationUtils.FetchAndVerifyActionResponse(t, pc.ExtPort, iData.InstructionID, types.Threshold, MyOp, MyCommand, pc.TeeID)
	require.Equal(t, "Action (type: instruction) processed successfully", res.Result.Log)
}
