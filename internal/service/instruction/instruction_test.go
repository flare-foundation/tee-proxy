package instruction

import (
	"context"
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/go-flare-common/pkg/policy"
)

type testMeta struct{}

func (*testMeta) Cosigners(_ *instruction.DataFixed) (map[common.Address]bool, uint64) {
	return map[common.Address]bool{}, 0
}

func (*testMeta) CheckConsistency(_ *instruction.Data, _ common.Address) error {
	return nil
}

func (*testMeta) ThresholdBIPS(_ *instruction.DataFixed) (int, error) {
	return -1, nil
}

func TestVoting(t *testing.T) {
	teeID := common.HexToAddress("dead")

	mr, c, s, sk4 := setupInstructionService(t, teeID, testutil.TestSigningPolicy)

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	go func() {
		err := s.Forward(t.Context())
		if err != nil {
			return
		}
	}()

	iData := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("TestVoting")),
			TeeID:                  teeID,
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.FTDC.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	iData.AdditionalVariableMessage = hexutil.Bytes("ADD_VAR_1")
	h, err := iData.HashForSigning()
	require.NoError(t, err)

	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	i1 := &instruction.Instruction{
		Data:      *iData,
		Signature: s1,
	}

	iData.AdditionalVariableMessage = hexutil.Bytes("ADD_VAR_2")
	h, err = iData.HashForSigning()
	require.NoError(t, err)

	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	i2 := &instruction.Instruction{
		Data:      *iData,
		Signature: s2,
	}

	sr1, err := s.ServeInstruction(t.Context(), i1)
	require.NoError(t, err)
	require.Equal(t, uint64(0), sr1.Receipt.Sequence)

	sr2, err := s.ServeInstruction(t.Context(), i2)
	require.NoError(t, err)
	require.Equal(t, uint64(1), sr2.Receipt.Sequence)

	pubKey1, err := sr1.RecoverPubKey()
	require.NoError(t, err)
	require.True(t, pubKey1.X.Cmp(sk4.X) == 0 && pubKey1.Y.Cmp(sk4.Y) == 0)

	pubKey2, err := sr2.RecoverPubKey()
	require.NoError(t, err)
	require.True(t, pubKey2.X.Cmp(sk4.X) == 0 && pubKey2.Y.Cmp(sk4.Y) == 0)

	time.Sleep(2000 * time.Millisecond)
	a, err := s.aq.Dequeue(t.Context(), queue.Main)
	require.NoError(t, err)
	require.Equal(t, a.Data.ID, iData.InstructionID)
	require.Equal(t, a.Data.SubmissionTag, types.Threshold)
	require.Equal(t, a.Data.Type, types.Instruction)

	// --------------------------------
	require.Len(t, a.Signatures, 2)
	require.Contains(t, a.Signatures, hexutil.Bytes(s1))
	require.Contains(t, a.Signatures, hexutil.Bytes(s2))

	require.Equal(t, a.AdditionalVariableMessages[0], hexutil.Bytes("ADD_VAR_1"))
	require.Equal(t, a.AdditionalVariableMessages[1], hexutil.Bytes("ADD_VAR_2"))
}

func TestStatus(t *testing.T) {
	teeID := common.HexToAddress("dead")

	mr, c, s, _ := setupInstructionService(t, teeID, testutil.TestSigningPolicy)

	defer mr.Close()
	defer c.Close() //nolint:errcheck

	go func() {
		err := s.Forward(t.Context())
		if err != nil {
			return
		}
	}()

	iData := &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte("TestStatus")),
			TeeID:                  teeID,
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.FTDC.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        []byte("TODO"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}

	h, err := iData.HashForSigning()
	require.NoError(t, err)

	s1, err := instruction.SignInstructionHash(h, testutil.PrivKey1)
	require.NoError(t, err)

	s2, err := instruction.SignInstructionHash(h, testutil.PrivKey2)
	require.NoError(t, err)

	i1 := &instruction.Instruction{
		Data:      *iData,
		Signature: s1,
	}

	i2 := &instruction.Instruction{
		Data:      *iData,
		Signature: s2,
	}

	sr1, err := s.ServeInstruction(t.Context(), i1)
	require.NoError(t, err)
	require.Equal(t, uint64(0), sr1.Receipt.Sequence)

	// Get the status of the instruction
	status, err := s.Status(i1.Data.InstructionID, 1)
	require.NoError(t, err)

	require.Equal(t, i1.Data.InstructionID, status.InstructionID)
	require.Equal(t, 1, len(status.Status))

	iHash, err := iData.HashFixed()
	require.NoError(t, err)
	require.Equal(t, status.Status[0].InstructionHash, iHash)
	require.Equal(t, status.Status[0].Finalized, false)
	require.Equal(t, status.Status[0].Deleted, false)
	require.Equal(t, status.Status[0].Weight, uint16(1))

	// * --------------------------------
	sr2, err := s.ServeInstruction(t.Context(), i2)
	require.NoError(t, err)

	require.Equal(t, uint64(1), sr2.Receipt.Sequence)

	// Get the status of the instruction
	status, err = s.Status(i2.Data.InstructionID, 1)
	require.NoError(t, err)

	require.Equal(t, i2.Data.InstructionID, status.InstructionID)
	require.Equal(t, 1, len(status.Status))

	iHash, err = iData.HashFixed()
	require.NoError(t, err)
	require.Equal(t, status.Status[0].InstructionHash, iHash)
	require.Equal(t, status.Status[0].Finalized, true)
	require.Equal(t, status.Status[0].Deleted, false)
	require.Equal(t, status.Status[0].Weight, uint16(4))

	// * --------------------------------
	iData2 := *iData
	iData2.OriginalMessage = []byte("TODO2")

	h2, err := iData2.HashForSigning()
	require.NoError(t, err)

	s3, err := instruction.SignInstructionHash(h2, testutil.PrivKey3)
	require.NoError(t, err)

	i3 := &instruction.Instruction{
		Data:      iData2,
		Signature: s3,
	}

	sr3, err := s.ServeInstruction(t.Context(), i3)
	require.NoError(t, err)

	require.Equal(t, uint64(0), sr3.Receipt.Sequence)

	// Get the status of the instruction
	status, err = s.Status(i3.Data.InstructionID, 1)
	require.NoError(t, err)

	require.Equal(t, i3.Data.InstructionID, status.InstructionID)
	require.Equal(t, 2, len(status.Status))

	iHash, err = iData2.HashFixed()
	require.NoError(t, err)

	if status.Status[0].InstructionHash == iHash {
		require.Equal(t, status.Status[0].Finalized, false)
		require.Equal(t, status.Status[0].Deleted, false)
		require.Equal(t, status.Status[0].Weight, uint16(3))
	} else if status.Status[1].InstructionHash == iHash {
		require.Equal(t, status.Status[1].Finalized, false)
		require.Equal(t, status.Status[1].Deleted, false)
		require.Equal(t, status.Status[1].Weight, uint16(3))
	} else {
		require.Fail(t, "unexpected instruction hash")
	}
}

func TestOPTypeOPCommandValidation(t *testing.T) {
	teeID := common.HexToAddress("dead")
	mr, c, s, _ := setupInstructionService(t, teeID, testutil.TestSigningPolicy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	// Valid opType/opCommand combinations - these should pass validation
	validTestCases := []struct {
		name        string
		opType      common.Hash
		opCommand   common.Hash
		description string
	}{
		{"Reg_TEEAttestation", op.Reg.Hash(), op.TEEAttestation.Hash(), "Reg + TEEAttestation should be valid"},
		{"Wallet_KeyDataProviderRestore", op.Wallet.Hash(), op.KeyDataProviderRestore.Hash(), "Wallet + KeyDataProviderRestore should be valid"},
		{"Wallet_KeyDataProviderRestoreTest", op.Wallet.Hash(), op.KeyDataProviderRestoreTest.Hash(), "Wallet + KeyDataProviderRestoreTest should be valid"},
		{"Wallet_KeyDelete", op.Wallet.Hash(), op.KeyDelete.Hash(), "Wallet + KeyDelete should be valid"},
		{"Wallet_KeyGenerate", op.Wallet.Hash(), op.KeyGenerate.Hash(), "Wallet + KeyGenerate should be valid"},
		{"XRP_Pay", op.XRP.Hash(), op.Pay.Hash(), "XRP + Pay should be valid"},
		{"XRP_Reissue", op.XRP.Hash(), op.Reissue.Hash(), "XRP + Reissue should be valid"},
		{"BTC_Pay", op.BTC.Hash(), op.Pay.Hash(), "BTC + Pay should be valid"},
		{"BTC_Reissue", op.BTC.Hash(), op.Reissue.Hash(), "BTC + Reissue should be valid"},
		{"FTDC_Prove", op.FTDC.Hash(), op.Prove.Hash(), "FTDC + Prove should be valid"},
	}

	// Constraint violations - these fail due to "non instruction opCommand" constraint in constraints.go
	constraintViolationTestCases := []struct {
		name        string
		opType      common.Hash
		opCommand   common.Hash
		description string
	}{
		{"Get_KeyInfo", op.Get.Hash(), op.KeyInfo.Hash(), "Get + KeyInfo should be invalid (non instruction opCommand)"},
		{"Get_TEEBackup", op.Get.Hash(), op.TEEBackup.Hash(), "Get + TEEBackup should be invalid (non instruction opCommand)"},
		{"Get_TEEInfo", op.Get.Hash(), op.TEEInfo.Hash(), "Get + TEEInfo should be invalid (non instruction opCommand)"},
		{"Policy_InitializePolicy", op.Policy.Hash(), op.InitializePolicy.Hash(), "Policy + InitializePolicy should be invalid (non instruction opCommand)"},
		{"Policy_UpdatePolicy", op.Policy.Hash(), op.UpdatePolicy.Hash(), "Policy + UpdatePolicy should be invalid (non instruction opCommand)"},
	}

	// Invalid opType/opCommand pairs - these fail due to incompatible type/command combinations
	invalidPairTestCases := []struct {
		name        string
		opType      common.Hash
		opCommand   common.Hash
		description string
	}{
		{"Wallet_Pay", op.Wallet.Hash(), op.Pay.Hash(), "Wallet + Pay should be invalid"},
		{"Wallet_Prove", op.Wallet.Hash(), op.Prove.Hash(), "Wallet + Prove should be invalid"},
		{"XRP_KeyGenerate", op.XRP.Hash(), op.KeyGenerate.Hash(), "XRP + KeyGenerate should be invalid"},
		{"XRP_TEEAttestation", op.XRP.Hash(), op.TEEAttestation.Hash(), "XRP + TEEAttestation should be invalid"},
		{"FTDC_Pay", op.FTDC.Hash(), op.Pay.Hash(), "FTDC + Pay should be invalid"},
		{"FTDC_KeyGenerate", op.FTDC.Hash(), op.KeyGenerate.Hash(), "FTDC + KeyGenerate should be invalid"},
		{"Get_Pay", op.Get.Hash(), op.Pay.Hash(), "Get + Pay should be invalid"},
		{"Policy_KeyGenerate", op.Policy.Hash(), op.KeyGenerate.Hash(), "Policy + KeyGenerate should be invalid"},
		{"Reg_Pay", op.Reg.Hash(), op.Pay.Hash(), "Reg + Pay should be invalid"},
		{"BTC_Prove", op.BTC.Hash(), op.Prove.Hash(), "BTC + Prove should be invalid"},
	}

	validCount := 0
	constraintViolationCount := 0
	invalidPairCount := 0

	// Helper function to create and test an instruction
	testInstruction := func(name string, opType, opCommand common.Hash, description string) (*instruction.Instruction, error) {
		iData := &instruction.Data{
			DataFixed: instruction.DataFixed{
				InstructionID:          crypto.Keccak256Hash([]byte("test_" + name)),
				TeeID:                  teeID,
				Timestamp:              uint64(time.Now().Unix()),
				RewardEpochID:          1,
				OPType:                 opType,
				OPCommand:              opCommand,
				OriginalMessage:        []byte("TEST_MESSAGE"),
				AdditionalFixedMessage: hexutil.Bytes{},
			},
			AdditionalVariableMessage: hexutil.Bytes{},
		}

		hash, err := iData.HashForSigning()
		require.NoError(t, err, "Failed to generate hash for signing for %s", name)

		signature, err := instruction.SignInstructionHash(hash, testutil.PrivKey1)
		require.NoError(t, err, "Failed to sign instruction hash for %s", name)

		inst := &instruction.Instruction{
			Data:      *iData,
			Signature: signature,
		}

		_, err = s.ServeInstruction(context.Background(), inst)
		return inst, err
	}

	// Test valid combinations - these should pass
	t.Log("Testing valid opType/opCommand combinations...")
	for _, tc := range validTestCases {
		_, err := testInstruction(tc.name, tc.opType, tc.opCommand, tc.description)
		require.NoError(t, err, "Expected valid combination to pass: %s (%s)", tc.name, tc.description)
		validCount++
	}

	// Test constraint violations - these should fail with "non instruction opCommand" error
	t.Log("Testing constraint violation cases...")
	for _, tc := range constraintViolationTestCases {
		_, err := testInstruction(tc.name, tc.opType, tc.opCommand, tc.description)
		require.Error(t, err, "Expected constraint violation to fail: %s (%s)", tc.name, tc.description)
		require.Contains(t, err.Error(), "non instruction opCommand", "Expected 'non instruction opCommand' error for: %s", tc.name)
		require.Contains(t, err.Error(), "'bad request'", "Expected 400 status code error for: %s", tc.name)
		constraintViolationCount++
	}

	// Test invalid pairs - these should fail with "invalid pair opType, opCommand" error
	t.Log("Testing invalid opType/opCommand pair cases...")
	for _, tc := range invalidPairTestCases {
		_, err := testInstruction(tc.name, tc.opType, tc.opCommand, tc.description)
		require.Error(t, err, "Expected invalid pair to fail: %s (%s)", tc.name, tc.description)
		require.Contains(t, err.Error(), "invalid pair opType, opCommand", "Expected 'invalid pair opType, opCommand' error for: %s", tc.name)
		require.Contains(t, err.Error(), "'bad request'", "Expected 400 status code error for: %s", tc.name)
		invalidPairCount++
	}

	// Summary logging
	t.Logf("Successfully validated %d valid opType/opCommand pairs", validCount)
	t.Logf("Successfully validated %d constraint violation cases", constraintViolationCount)
	t.Logf("Successfully validated %d invalid opType/opCommand pairs", invalidPairCount)
	t.Logf("Total test cases: %d", validCount+constraintViolationCount+invalidPairCount)
}

// Helper function to create a base instruction data with common fields
func createBaseInstructionData(testName string, teeID common.Address) *instruction.Data {
	return &instruction.Data{
		DataFixed: instruction.DataFixed{
			InstructionID:          crypto.Keccak256Hash([]byte(testName)),
			TeeID:                  teeID,
			Timestamp:              uint64(time.Now().Unix()),
			RewardEpochID:          1,
			OPType:                 op.FTDC.Hash(),
			OPCommand:              op.Prove.Hash(),
			OriginalMessage:        []byte("TEST_MESSAGE"),
			AdditionalFixedMessage: hexutil.Bytes{},
		},
		AdditionalVariableMessage: hexutil.Bytes{},
	}
}

// Helper function to sign an instruction with a given private key
func signInstruction(t *testing.T, iData *instruction.Data, privateKey *ecdsa.PrivateKey) *instruction.Instruction {
	hash, err := iData.HashForSigning()
	require.NoError(t, err)

	signature, err := instruction.SignInstructionHash(hash, privateKey)
	require.NoError(t, err)

	return &instruction.Instruction{
		Data:      *iData,
		Signature: signature,
	}
}

func TestVotingStorageErrors(t *testing.T) {
	teeID := common.HexToAddress("dead")
	mr, c, s, _ := setupInstructionService(t, teeID, testutil.TestSigningPolicy)
	defer mr.Close()
	defer c.Close() //nolint:errcheck

	testCases := []struct {
		name           string
		setupFunc      func(t *testing.T, s *Service) *instruction.Instruction
		expectedError  string
		expectedStatus string
	}{
		{
			name: "WrongTeeID_400",
			setupFunc: func(t *testing.T, s *Service) *instruction.Instruction {
				iData := createBaseInstructionData("test_wrong_tee_id", teeID)
				iData.TeeID = common.HexToAddress("wrong") // Wrong teeID
				return signInstruction(t, iData, testutil.PrivKey1)
			},
			expectedError:  "wrong teeID",
			expectedStatus: "'bad request'",
		},
		{
			name: "NonExistentRewardEpoch_404",
			setupFunc: func(t *testing.T, s *Service) *instruction.Instruction {
				iData := createBaseInstructionData("test_nonexistent_epoch", teeID)
				iData.RewardEpochID = 999 // Non-existent epoch
				return signInstruction(t, iData, testutil.PrivKey1)
			},
			expectedError:  "no round 999",
			expectedStatus: "'not found'",
		},
		{
			name: "VoterNotInSigningPolicy_403",
			setupFunc: func(t *testing.T, s *Service) *instruction.Instruction {
				iData := createBaseInstructionData("test_invalid_voter", teeID)
				randomKey, err := crypto.GenerateKey()
				require.NoError(t, err)
				return signInstruction(t, iData, randomKey) // Use key not in signing policy
			},
			expectedError:  "voter not registered",
			expectedStatus: "'forbidden'",
		},
		{
			name: "AlreadyVotedSigner_403",
			setupFunc: func(t *testing.T, s *Service) *instruction.Instruction {
				iData := createBaseInstructionData("test_duplicate_vote", teeID)
				inst := signInstruction(t, iData, testutil.PrivKey1)

				// Process the instruction once (should succeed)
				_, err := s.ServeInstruction(context.Background(), inst)
				require.NoError(t, err, "First vote should succeed")

				// Return the same instruction to try voting again
				return inst
			},
			expectedError:  "signature already stored",
			expectedStatus: "'forbidden'",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instruction := tc.setupFunc(t, s)

			_, err := s.ServeInstruction(context.Background(), instruction)

			require.Error(t, err, "Expected error for %s", tc.name)
			require.Contains(t, err.Error(), tc.expectedError, "Expected specific error message for %s", tc.name)
			require.Contains(t, err.Error(), tc.expectedStatus, "Expected specific status code for %s", tc.name)

			t.Logf("✓ %s: Got expected error: %v", tc.name, err)
		})
	}

	t.Logf("Successfully validated %d voting storage error scenarios", len(testCases))
}

func setupInstructionService(t *testing.T, teeID common.Address, sp *policy.SigningPolicy) (*miniredis.Miniredis, *redis.Client, *Service, *ecdsa.PrivateKey) {
	mr := miniredis.RunT(t)
	c := queue.NewClient(mr.Addr())

	vCfg := &voting.Config{
		ProposalExpiration: 0,
		MaxPendingRequests: 0,
	}

	vs := NewStorage(vCfg, 3, &testMeta{}, 3)
	vs.CreateRound(sp)

	sk4, err := crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}

	aq := queue.NewActionQueues(c)
	s := &Service{
		teeID:    teeID,
		vs:       vs,
		policies: make(chan policy.SigningPolicy, 1),
		aq:       aq,
		pk:       sk4,
	}

	return mr, c, s, sk4
}
