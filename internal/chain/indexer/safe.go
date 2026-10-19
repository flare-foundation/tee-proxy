package indexer

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
)

// safeApprovals returns the Safe approval candidates for the list (extensionID,
// nonce) in the (fromBlock, toBlock] window containing its activation: one per
// MachinePathListApproved log from safeAddress whose emitting execTransaction is
// indexed, at the Safe nonce the event records. The caller binds a candidate to
// the list's content.
func (s *Source) safeApprovals(ctx context.Context, extensionID common.Hash, nonce uint64, safeAddress common.Address, fromBlock, toBlock int64) ([]safe.Approval, error) {
	logs, err := database.FetchLogsByAddressAndTopic0BlockNumber(ctx, s.db, database.LogsParams{
		Address: s.addresses.MachinePathManager,
		Topic0:  machinePathListApprovedEventSel,
		From:    fromBlock,
		To:      toBlock,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching MachinePathListApproved logs: %w", err)
	}

	extIDBig := new(big.Int).SetBytes(extensionID[:])
	nonceBig := new(big.Int).SetUint64(nonce)

	approvals := make([]safe.Approval, 0, len(logs))
	for i := range logs {
		ethLog, err := logs[i].ToEthLog()
		if err != nil {
			return nil, fmt.Errorf("converting MachinePathListApproved log: %w", err)
		}
		event, err := filterer.ParseMachinePathListApproved(ethLog)
		if err != nil {
			return nil, fmt.Errorf("parsing MachinePathListApproved event: %w", err)
		}

		// skip other lists' and Safes' approvals before fetching their transactions
		if event.ExtensionId.Cmp(extIDBig) != 0 || event.Nonce.Cmp(nonceBig) != 0 || event.Safe != safeAddress {
			continue
		}

		// the Safe execTransaction that emitted the approval carries the owner signatures
		tx, ok, err := s.execTransactionByHash(ctx, safeAddress, logs[i].BlockNumber, logs[i].TransactionHash)
		if err != nil {
			return nil, err
		}
		if !ok {
			logger.Debugf("safe approval log %s@%d: no matching execTransaction to safe %s in block", logs[i].TransactionHash, logs[i].BlockNumber, safeAddress)
			continue
		}

		input, err := hex.DecodeString(tx.Input)
		if err != nil {
			logger.Debugf("safe approval tx %s: undecodable input: %v", tx.Hash, err)
			continue
		}
		inputs, err := safe.DecodeExecTransactionCalldata(input)
		if err != nil {
			logger.Debugf("safe approval tx %s is not execTransaction calldata: %v", tx.Hash, err)
			continue
		}

		approvals = append(approvals, safe.Approval{ExecTransaction: inputs, Nonce: uint64(event.SafeNonce)})
	}

	return approvals, nil
}

// execTransactionByHash fetches the Safe execTransactions in the given block
// and returns the one whose transaction hash matches txHash.
func (s *Source) execTransactionByHash(ctx context.Context, safeAddress common.Address, blockNumber uint64, txHash string) (database.Transaction, bool, error) {
	txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, s.db, database.TxParams{
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
