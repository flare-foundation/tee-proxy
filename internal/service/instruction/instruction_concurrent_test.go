package instruction

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/stretchr/testify/require"
)

// Note: Even when starting the two threads at the same time, the first thread will be processed faster so we don't really create the best race condition here.
func TestConcurrentVoteBoxCreation(t *testing.T) {
	teeID := common.HexToAddress("dead")
	mr, c, s := setupInstructionService(t, teeID, testutil.TestSigningPolicy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	// Two identical instructions arrive simultaneously
	// Expected: Both should be processed, but only one vote per signer should be recorded

	// Create base instruction data
	iData := createBaseInstructionData("concurrent_same_same", teeID)

	// Create two identical instructions with different signers
	inst1 := signInstruction(t, iData, testutil.PrivKey1)
	inst2 := signInstruction(t, iData, testutil.PrivKey2)

	// Set up concurrent execution
	var wg sync.WaitGroup
	startChan := make(chan struct{})
	results := make(chan error, 2)

	// Launch first goroutine
	wg.Go(func() {
		<-startChan // Wait for signal to start
		_, err := s.ServeInstruction(context.Background(), inst1)
		results <- err
	})

	// Launch second goroutine
	wg.Go(func() {
		<-startChan // Wait for signal to start
		_, err := s.ServeInstruction(context.Background(), inst2)
		results <- err
	})

	// Start both goroutines simultaneously
	close(startChan)

	// Wait for completion
	wg.Wait()
	close(results)

	// Collect results
	errors := make([]error, 0, len(results))
	for err := range results {
		errors = append(errors, err)
	}

	// Both should succeed (different signers)
	require.Len(t, errors, 2)
	require.NoError(t, errors[0], "First instruction should succeed")
	require.NoError(t, errors[1], "Second instruction should succeed")

	// Verify vote box state
	status, err := s.Statuses(iData.InstructionID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, len(status.Status), "Should have one vote box")
	// PrivKey1 has weight 1, PrivKey2 has weight 3, so total should be 4
	require.Equal(t, uint16(4), status.Status[0].Weight, "Should have weight from both votes (1+3=4)")

	t.Logf("✓ Concurrent identical instructions processed successfully")
}

// TestConcurrentVoteBoxCreation_RaceConditionStress tests race conditions in AddVote function
// with 100 pairs of concurrent instructions to maximize probability of race conditions.
// This test specifically focuses on ensuring no duplicate vote boxes are created.
func TestConcurrentVoteBoxCreationDifferentInstHash(t *testing.T) {
	teeID := common.HexToAddress("sameInstIDDiffHash")
	mr, c, s := setupInstructionService(t, teeID, testutil.MockSigningPolicy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	// Use 100 pairs of instructions to maximize race condition probability
	const numPairs = 100
	const totalInstructions = numPairs * 2

	t.Logf("Starting race condition stress test with %d instruction pairs (%d total instructions)", numPairs, totalInstructions)

	// Create instruction pairs that should create race conditions in AddVote
	type instructionPair struct {
		id       string
		inst1    *instruction.Instruction
		inst2    *instruction.Instruction
		baseData *instruction.Data
	}

	instructionPairs := make([]instructionPair, 0, numPairs)

	// Create pairs where each pair has:
	// 1. Same InstructionID (will compete for the same voteBoxes map slot)
	// 2. Different FixedVariableMessage (will create different hashes, different vote boxes)
	for i := range numPairs {
		baseInstID := crypto.Keccak256Hash(fmt.Appendf(nil, "race_test_pair_%d", i))

		// Create base instruction data with unique InstructionID for this pair
		iData := &instruction.Data{
			DataFixed: instruction.DataFixed{
				InstructionID:          baseInstID,
				TeeID:                  teeID,
				Timestamp:              uint64(time.Now().Unix()) + uint64(i%10), // Slight time variation
				RewardEpochID:          1,
				OPType:                 op.FDC2.Hash(),
				OPCommand:              op.Prove.Hash(),
				OriginalMessage:        []byte("RACE_TEST_MESSAGE"),
				AdditionalFixedMessage: hexutil.Bytes{},
			},
			AdditionalVariableMessage: hexutil.Bytes{},
		}

		// Create two variations with different AdditionalVariableMessage
		iData1 := *iData
		iData1.AdditionalFixedMessage = hexutil.Bytes(fmt.Sprintf("VAR_MSG_1_%d", i))

		iData2 := *iData
		iData2.AdditionalFixedMessage = hexutil.Bytes(fmt.Sprintf("VAR_MSG_2_%d", i))

		// Sign with different keys to ensure different signers
		keyIndex1 := (i * 2) % len(testutil.MockPrivKeys)
		keyIndex2 := (i*2 + 1) % len(testutil.MockPrivKeys)

		inst1 := signInstruction(t, &iData1, testutil.MockPrivKeys[keyIndex1])
		inst2 := signInstruction(t, &iData2, testutil.MockPrivKeys[keyIndex2])

		instructionPairs = append(instructionPairs, instructionPair{
			id:       fmt.Sprintf("pair_%d", i),
			inst1:    inst1,
			inst2:    inst2,
			baseData: iData,
		})
	}

	// Set up concurrent execution with maximum race condition potential
	var wg sync.WaitGroup
	startChan := make(chan struct{})

	// Launch all instructions simultaneously to maximize race conditions
	for _, pair := range instructionPairs {
		// Launch first instruction of the pair
		wg.Add(1)
		go func(inst *instruction.Instruction) {
			defer wg.Done()
			<-startChan // Wait for signal to start
			_, err := s.ServeInstruction(t.Context(), inst)
			require.NoError(t, err)
		}(pair.inst1)

		// Launch second instruction of the pair
		wg.Add(1)
		go func(inst *instruction.Instruction) {
			defer wg.Done()
			<-startChan // Wait for signal to start
			_, err := s.ServeInstruction(t.Context(), inst)
			require.NoError(t, err)
		}(pair.inst2)
	}

	// Start all goroutines simultaneously to maximize race condition probability
	startTime := time.Now()
	close(startChan)

	// Wait for all to complete
	wg.Wait()

	duration := time.Since(startTime)
	t.Logf("All instructions completed in %v", duration)

	// Verify that each instruction pair created exactly 2 vote boxes (one for each different hash)
	// This is the critical test: ensuring no duplicate vote boxes were created due to race conditions
	for i, pair := range instructionPairs {
		status, err := s.Statuses(pair.baseData.InstructionID, 1)
		require.NoError(t, err, "Failed to get status for pair %d", i)

		// Should have exactly 2 vote boxes (one for each different AdditionalVariableMessage hash)
		require.Equal(t, 2, len(status.Status),
			"Pair %d should have exactly 2 vote boxes, got %d. This indicates a race condition may have occurred.",
			i, len(status.Status))

		// Verify that each vote box has exactly one vote (from one signer each)
		for j, voteStatus := range status.Status {
			require.Greater(t, voteStatus.Weight, uint16(0),
				"Vote box %d in pair %d should have weight 1 (one vote), got %d",
				j, i, voteStatus.Weight)
		}
	}
}

func TestConcurrentVoteBoxCreationHighLoad(t *testing.T) {
	teeID := common.HexToAddress("dead")
	mr, c, s := setupInstructionService(t, teeID, testutil.MockSigningPolicy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	// Simulate high load: many votes for same instruction arriving simultaneously
	const numGoroutines = 100

	// Create base instruction
	iData := createBaseInstructionData("high_load_same", teeID)

	// Create different signatures for the same instruction
	instructions := make([]*instruction.Instruction, 0, numGoroutines)

	for i := range numGoroutines {
		dataVariation := *iData
		dataVariation.AdditionalVariableMessage = hexutil.Bytes([]byte{byte(i)})
		inst := signInstruction(t, &dataVariation, testutil.MockPrivKeys[i])
		instructions = append(instructions, inst)
	}

	// Set up concurrent execution
	var wg sync.WaitGroup
	startChan := make(chan struct{})
	results := make(chan error, numGoroutines)

	// Launch multiple goroutines
	for i := range numGoroutines {
		wg.Add(1)
		go func(inst *instruction.Instruction) {
			defer wg.Done()
			<-startChan
			_, err := s.ServeInstruction(context.Background(), inst)
			results <- err
		}(instructions[i])
	}

	// Start all goroutines simultaneously
	close(startChan)
	wg.Wait()
	close(results)

	// Collect and analyze results
	errors := make([]error, 0, len(results))

	successCount := 0
	duplicateCount := 0

	for err := range results {
		errors = append(errors, err)
		if err == nil {
			successCount++
		} else if strings.Contains(err.Error(), "signature already stored") {
			duplicateCount++
		} else {
			t.Errorf("Error: %v", err)
		}
	}

	require.Equal(t, numGoroutines, len(errors), "Should have result for each goroutine")

	// We expect some successes and some duplicates due to same signers
	t.Logf("High load results: %d success, %d duplicates out of %d total",
		successCount, duplicateCount, numGoroutines)

	// At least one should succeed
	require.Greater(t, successCount, 0, "At least one instruction should succeed")

	// Verify final state
	status, err := s.Statuses(iData.InstructionID, 1)
	require.NoError(t, err)

	totalWeight := uint16(0)
	for _, boxStatus := range status.Status {
		totalWeight += boxStatus.Weight
	}

	t.Logf("Final state: %d vote boxes, total weight: %d", len(status.Status), totalWeight)
	require.Greater(t, int(totalWeight), 0, "Should have some weight from successful votes")

	expectedMaxWeight := 65535 // TotalWeight from MockSigningPolicy
	require.LessOrEqual(t, int(totalWeight), expectedMaxWeight, "Weight shouldn't exceed total policy weight")

	t.Logf("Average weight per successful vote: %.2f", float64(totalWeight)/float64(successCount))

	status, err = s.Statuses(iData.InstructionID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, len(status.Status), "Should have one vote box")
	require.Equal(t, testutil.MockSigningPolicy.Voters.TotalWeight, status.Status[0].Weight, "Should have weight from all voters")

	t.Logf("✓ Concurrent identical instructions processed successfully")
}

func TestConcurrentThresholdFinalization(t *testing.T) {
	teeID := common.HexToAddress("dead")
	mr, c, s := setupInstructionService(t, teeID, testutil.TestSigningPolicy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	// Start the Forward method to process threshold events
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		err := s.Forward(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Forward method error: %v", err)
		}
	}()

	// Test scenario: Two votes that would both cross threshold arrive simultaneously
	// Expected: Only one action should be enqueued, not two

	// Create instruction data
	iData := createBaseInstructionData("threshold_race", teeID)

	// First, add a vote that's below threshold (PrivKey1 has weight 1, threshold is 3)
	inst1 := signInstruction(t, iData, testutil.PrivKey1)
	_, err := s.ServeInstruction(context.Background(), inst1)
	require.NoError(t, err, "First vote should succeed")

	// Verify we're below threshold
	status, err := s.Statuses(iData.InstructionID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, len(status.Status), "Should have one vote box")
	require.Equal(t, uint16(1), status.Status[0].Weight, "Should have weight 1")
	require.False(t, status.Status[0].Finalized, "Should not be finalized yet")

	// Now create two votes that would each cross threshold if processed
	inst2 := signInstruction(t, iData, testutil.PrivKey2)
	inst3 := signInstruction(t, iData, testutil.PrivKey3)

	// Set up concurrent execution with more aggressive timing
	var wg sync.WaitGroup
	startChan := make(chan struct{})

	// Launch both threshold-crossing votes simultaneously
	wg.Go(func() {
		<-startChan
		_, err := s.ServeInstruction(context.Background(), inst2)
		require.NoError(t, err)
	})

	wg.Go(func() {
		<-startChan
		_, err := s.ServeInstruction(context.Background(), inst3)
		require.NoError(t, err)
	})

	// Start both goroutines simultaneously
	close(startChan)
	wg.Wait()

	// Verify final vote box state
	status, err = s.Statuses(iData.InstructionID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, len(status.Status), "Should still have one vote box")
	require.Equal(t, uint16(7), status.Status[0].Weight, "Should have combined weight (1+3+3=7)")
	require.True(t, status.Status[0].Finalized, "Should be finalized")

	var action *types.Action
	require.Eventually(t, func() bool {
		a, err := s.aq.Dequeue(context.Background(), processorutils.Main)
		if err != nil {
			return false
		}
		action = a
		return true
	}, 2*time.Second, 10*time.Millisecond, "threshold action was not enqueued")

	require.Equal(t, iData.InstructionID, action.Data.ID, "Action should have correct instruction ID")
	require.Equal(t, types.Threshold, action.Data.SubmissionTag, "Action should have Threshold tag")
	require.Equal(t, types.Instruction, action.Data.Type, "Action should be Instruction type")
	require.Len(t, action.Signatures, 2, "Queued action should contain two signatures has")

	require.Never(t, func() bool {
		_, err := s.aq.Dequeue(context.Background(), processorutils.Main)
		return err == nil
	}, 200*time.Millisecond, 20*time.Millisecond, "more than one threshold action was enqueued")

	t.Logf("✓ Concurrent threshold crossing handled correctly: 1 action enqueued")
}

func TestConcurrentThresholdFinalizationHighLoad(t *testing.T) {
	n := 100
	policy, privKeys := testutil.GeneratePolicy(n, false)

	teeID := common.HexToAddress("dead")
	mr, c, s := setupInstructionService(t, teeID, policy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	// Start the Forward method to process threshold events
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		err := s.Forward(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Forward method error: %v", err)
		}
	}()

	// Test scenario: 100 (n) votes for the same instruction simultaneously
	// Expected: Only one action should be enqueued with 51 votes

	// Create instruction data
	iData := createBaseInstructionData("threshold_race", teeID)

	// Set up concurrent execution with more aggressive timing
	var wg sync.WaitGroup
	wg.Add(n)
	startChan := make(chan struct{})

	// Launch both  votes simultaneously
	for j := range n {
		inst := signInstruction(t, iData, privKeys[j])
		go func(i instruction.Instruction) {
			defer wg.Done()
			<-startChan
			_, err := s.ServeInstruction(context.Background(), &i)
			require.NoError(t, err)
		}(*inst)
	}

	// Start both goroutines simultaneously
	close(startChan)
	wg.Wait()

	// Verify final vote box state
	status, err := s.Statuses(iData.InstructionID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, len(status.Status), "Should still have one vote box")
	require.Equal(t, policy.Voters.TotalWeight, status.Status[0].Weight, "Should have all votes")
	require.True(t, status.Status[0].Finalized, "Should be finalized")

	var action *types.Action
	require.Eventually(t, func() bool {
		a, err := s.aq.Dequeue(context.Background(), processorutils.Main)
		if err != nil {
			return false
		}
		action = a
		return true
	}, 2*time.Second, 10*time.Millisecond, "threshold action was not enqueued")

	require.Equal(t, iData.InstructionID, action.Data.ID, "Action should have correct instruction ID")
	require.Equal(t, types.Threshold, action.Data.SubmissionTag, "Action should have Threshold tag")
	require.Equal(t, types.Instruction, action.Data.Type, "Action should be Instruction type")
	require.Len(t, action.Signatures, n/2+1, "wrong number of signatures")

	require.Never(t, func() bool {
		_, err := s.aq.Dequeue(context.Background(), processorutils.Main)
		return err == nil
	}, 200*time.Millisecond, 20*time.Millisecond, "more than one threshold action was enqueued")

	t.Logf("✓ Concurrent threshold crossing handled correctly: 1 action enqueued")
}
