package machinepath

import (
	"bytes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	"github.com/flare-foundation/tee-node/pkg/types"
)

// selectApproval returns the first candidate that approves exactly this list —
// an approveMachinePathList CALL to managerAddress binding (extensionID, nonce,
// messageHash) — and verifies with exactly the check the TEE node applies
// (safe.Approval.Verify against gov.Signers/gov.Threshold at the candidate's
// Safe nonce). It returns nil (no error) when none does.
func selectApproval(
	candidates []safe.Approval,
	gov types.Governance,
	managerAddress common.Address,
	extensionID common.Hash,
	nonce uint64,
	messageHash common.Hash,
	chainID uint64,
) (*safe.Approval, error) {
	expectedData, err := types.ApproveMachinePathListCalldata(extensionID, nonce, messageHash)
	if err != nil {
		return nil, fmt.Errorf("building expected approveMachinePathList calldata: %w", err)
	}

	for i := range candidates {
		approval := candidates[i]
		if !matchApproval(approval.ExecTransaction, expectedData, managerAddress) {
			logger.Debugf("safe approval at safeNonce %d is not the expected approveMachinePathList call", approval.Nonce)
			continue
		}

		// forward only evidence the node will accept
		if err := approval.Verify(chainID, gov.Safe, gov.Signers, gov.Threshold); err != nil {
			logger.Debugf("safe approval does not verify at safeNonce %d: %v", approval.Nonce, err)
			continue
		}

		return &approval, nil
	}

	return nil, nil
}

// matchApproval reports whether inputs is an approveMachinePathList CALL to the
// MachinePathManager binding the expected list content.
func matchApproval(inputs safe.ExecTransactionInputs, expectedData []byte, managerAddress common.Address) bool {
	return inputs.To == managerAddress && inputs.Operation == safe.OperationCall && bytes.Equal(inputs.Data, expectedData)
}
