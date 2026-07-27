package machinepath

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	"github.com/flare-foundation/tee-node/pkg/types"
	"gorm.io/gorm"
)

// collectSafeApproval returns the Safe approval evidence for the list
// (extensionID, nonce) under Safe-backed governance, or nil (no error) when
// none is verifiable yet.
//
// It locates approvals via MachinePathListApproved logs in the (fromBlock,
// toBlock] window that contains the activation, and the event carries the Safe
// nonce the owners signed at (safeNonce), so no scan or count is needed. The
// exact list and Safe are enforced downstream (matchApproval binds
// (extensionID, nonce, messageHash); execTransactionByHash scopes to gov.Safe),
// so other lists'/Safes' logs in the window are skipped. For each candidate it
// fetches the pinpointed Safe execTransaction for the owner signatures — the
// one thing not on chain in recoverable form — and pre-verifies them with
// exactly the check the TEE node applies (safe.Approval.Verify against
// gov.Signers/gov.Threshold at safeNonce). The first approval that verifies is
// returned.
func collectSafeApproval(
	ctx context.Context,
	db *gorm.DB,
	gov types.Governance,
	managerAddress common.Address,
	extensionID common.Hash,
	nonce uint64,
	messageHash common.Hash,
	chainID uint64,
	fromBlock, toBlock int64,
) (*safe.Approval, error) {
	expectedData, err := types.ApproveMachinePathListCalldata(extensionID, nonce, messageHash)
	if err != nil {
		return nil, fmt.Errorf("building expected approveMachinePathList calldata: %w", err)
	}

	// Fetch MachinePathListApproved logs in (fromBlock, toBlock] — the window
	// containing the approval that activated this list: the same block as the
	// MachinePathListSigned the caller located (pure-Safe path) or earlier
	// (mixed with ECDSA signatures). The block range keeps this bounded without
	// relying on log recency, matching collectSignatures. We filter only on the
	// event topic0 here; the exact list and Safe are enforced downstream —
	// matchApproval binds (extensionID, nonce, messageHash) via the expected
	// calldata, and execTransactionByHash scopes to gov.Safe.
	logs, err := database.FetchLogsByAddressAndTopic0BlockNumber(ctx, db, database.LogsParams{
		Address: managerAddress,
		Topic0:  machinePathListApprovedEventSel,
		From:    fromBlock,
		To:      toBlock,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching MachinePathListApproved logs: %w", err)
	}

	for i := range logs {
		approval, ok, err := approvalFromLog(ctx, db, gov, managerAddress, expectedData, chainID, &logs[i])
		if err != nil {
			return nil, err
		}
		if ok {
			return approval, nil
		}
	}

	return nil, nil
}

// approvalFromLog resolves one MachinePathListApproved log to verified approval
// evidence: it reads the Safe nonce the owners signed at from the event, fetches
// the Safe execTransaction the log was emitted from for the owner signatures,
// confirms the inner call is the expected approveMachinePathList, and
// pre-verifies the signatures at that nonce. ok is false (without error) when
// the log does not resolve to a locally verifiable approval.
func approvalFromLog(
	ctx context.Context,
	db *gorm.DB,
	gov types.Governance,
	managerAddress common.Address,
	expectedData []byte,
	chainID uint64,
	log *database.Log,
) (*safe.Approval, bool, error) {
	// The Safe nonce the owners signed at is recorded in the event (safeNonce),
	// so no scan or count is needed to reconstruct the SafeTx hash.
	ethLog, err := log.ToEthLog()
	if err != nil {
		return nil, false, fmt.Errorf("converting MachinePathListApproved log: %w", err)
	}
	event, err := filterer.ParseMachinePathListApproved(ethLog)
	if err != nil {
		return nil, false, fmt.Errorf("parsing MachinePathListApproved event: %w", err)
	}

	// The Safe execTransaction that emitted this approval is in the same block;
	// fetch it (matched by transaction hash) for the owner signatures.
	tx, ok, err := execTransactionByHash(ctx, db, gov.Safe, log.BlockNumber, log.TransactionHash)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		logger.Debugf("safe approval log %s@%d: no matching execTransaction to safe %s in block", log.TransactionHash, log.BlockNumber, gov.Safe)
		return nil, false, nil
	}

	inputs, ok := matchApproval(tx.Input, expectedData, managerAddress)
	if !ok {
		logger.Debugf("safe approval tx %s does not decode to the expected approveMachinePathList call", tx.Hash)
		return nil, false, nil
	}

	approval := &safe.Approval{ExecTransaction: inputs, Nonce: uint64(event.SafeNonce)}

	// Pre-verify with the node's exact check; forward only evidence the node
	// will accept.
	if err := approval.Verify(chainID, gov.Safe, gov.Signers, gov.Threshold); err != nil {
		logger.Debugf("safe approval tx %s does not verify at safeNonce %d from the event: %v", tx.Hash, event.SafeNonce, err)
		return nil, false, nil
	}
	return approval, true, nil
}

// execTransactionByHash fetches the Safe execTransactions in the given block
// and returns the one whose transaction hash matches txHash.
func execTransactionByHash(
	ctx context.Context,
	db *gorm.DB,
	safeAddress common.Address,
	blockNumber uint64,
	txHash string,
) (database.Transaction, bool, error) {
	txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, db, database.TxParams{
		ToAddress:   safeAddress,
		FunctionSel: safe.ExecTransactionSelector,
		From:        int64(blockNumber) - 1,
		To:          int64(blockNumber),
	})
	if err != nil {
		return database.Transaction{}, false, err
	}
	for i := range txs {
		if strings.EqualFold(strings.TrimPrefix(txs[i].Hash, "0x"), strings.TrimPrefix(txHash, "0x")) {
			return txs[i], true, nil
		}
	}
	return database.Transaction{}, false, nil
}

// matchApproval decodes a hex-encoded execTransaction calldata and reports
// whether it is an approveMachinePathList CALL to the MachinePathManager
// binding the expected list content.
func matchApproval(input string, expectedData []byte, managerAddress common.Address) (safe.ExecTransactionInputs, bool) {
	inputB, err := hex.DecodeString(input)
	if err != nil {
		return safe.ExecTransactionInputs{}, false
	}
	inputs, err := safe.DecodeExecTransactionCalldata(inputB)
	if err != nil {
		return safe.ExecTransactionInputs{}, false
	}
	if inputs.To != managerAddress || inputs.Operation != safe.OperationCall {
		return safe.ExecTransactionInputs{}, false
	}
	if !bytes.Equal(inputs.Data, expectedData) {
		return safe.ExecTransactionInputs{}, false
	}
	return inputs, true
}
