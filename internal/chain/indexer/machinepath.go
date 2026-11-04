package indexer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
)

const (
	txSuccessStatus = 1

	// recentSignedLogsLimit bounds the LatestSignedNonce query. It must cover
	// any out-of-order signing window: as long as no more than this many
	// lists are signed between the highest nonce being submitted and a later
	// query, the highest nonce > lastNonce will be among the results.
	recentSignedLogsLimit = 20
)

var (
	errListNotSigned = errors.New("no MachinePathListSigned event for list")
	errInvalidInputs = errors.New("invalid calldata inputs")
)

// LatestSignedNonce returns the highest nonce among recent MachinePathListSigned
// events for extensionID that is strictly greater than after.
func (s *Source) LatestSignedNonce(ctx context.Context, extensionID common.Hash, after uint64) (uint64, bool, error) {
	topics := [4]common.Hash{}
	topics[0] = machinePathListSignedEventSel
	topics[1] = extensionID

	logs, err := database.FetchLogsFull(ctx, s.db, database.LogsFullParams{
		Address: s.addresses.MachinePathManager,
		Topics:  topics,
		Number:  recentSignedLogsLimit,
	})
	if err != nil {
		return 0, false, err
	}

	var (
		nonce uint64
		found bool
	)
	for i := range logs {
		ethLog, err := logs[i].ToEthLog()
		if err != nil {
			return 0, false, fmt.Errorf("converting log: %w", err)
		}

		event, err := filterer.ParseMachinePathListSigned(ethLog)
		if err != nil {
			return 0, false, fmt.Errorf("parsing MachinePathListSigned event: %w", err)
		}
		if !event.Nonce.IsUint64() {
			continue
		}

		n := event.Nonce.Uint64()
		if n > after && (!found || n > nonce) {
			nonce = n
			found = true
		}
	}

	return nonce, found, nil
}

// SignedList returns the activated list (extensionID, nonce): its paths in on-chain
// order, the governance signatures of successful signMachinePathList transactions
// and, under Safe-backed governance, the Safe approvals — both taken from the blocks
// between the first added path and the list's activation.
func (s *Source) SignedList(ctx context.Context, extensionID common.Hash, nonce uint64, gov types.Governance) (machinepath.List, error) {
	toBlock, err := s.signedBlock(ctx, extensionID, nonce)
	if err != nil {
		return machinepath.List{}, err
	}

	paths, fromBlock, err := s.fetchPaths(ctx, extensionID, nonce)
	if err != nil {
		return machinepath.List{}, fmt.Errorf("fetching machine paths: %w", err)
	}
	if len(paths) == 0 {
		return machinepath.List{}, nil
	}

	// windows are (from, to]: start one block below the first added path
	from := int64(fromBlock) - 1
	to := int64(toBlock)

	sigs, err := s.signatures(ctx, extensionID, nonce, from, to)
	if err != nil {
		return machinepath.List{}, fmt.Errorf("collecting governance signatures: %w", err)
	}

	list := machinepath.List{Paths: paths, Signatures: sigs}
	if gov.Safe != (common.Address{}) {
		list.Approvals, err = s.safeApprovals(ctx, extensionID, nonce, gov.Safe, from, to)
		if err != nil {
			return machinepath.List{}, fmt.Errorf("collecting safe approvals: %w", err)
		}
	}

	return list, nil
}

// signedBlock returns the block in which the list (extensionID, nonce) was activated.
func (s *Source) signedBlock(ctx context.Context, extensionID common.Hash, nonce uint64) (uint64, error) {
	topics := [4]common.Hash{}
	topics[0] = machinePathListSignedEventSel
	topics[1] = extensionID
	topics[2] = convert.Uint64ToHash(nonce)

	logs, err := database.FetchLogsFull(ctx, s.db, database.LogsFullParams{
		Address: s.addresses.MachinePathManager,
		Topics:  topics,
		Number:  1,
	})
	if err != nil {
		return 0, fmt.Errorf("fetching MachinePathListSigned event: %w", err)
	}
	if len(logs) == 0 {
		return 0, errListNotSigned
	}

	return logs[0].BlockNumber, nil
}

// fetchPaths returns the machine paths of the list (extensionID, nonce) in the
// order they were added on chain, together with the earliest block in which a
// path was added. The order matches the on-chain message hash construction.
func (s *Source) fetchPaths(ctx context.Context, extensionID common.Hash, nonce uint64) ([]cmpaths.IMachinePathManagerMachinePath, uint64, error) {
	topics := [4]common.Hash{}
	topics[0] = machinePathsAddedEventSel
	topics[1] = extensionID
	topics[2] = convert.Uint64ToHash(nonce)

	logs, err := database.FetchLogsFull(ctx, s.db, database.LogsFullParams{
		Address: s.addresses.MachinePathManager,
		Topics:  topics,
		Number:  -1,
	})
	if err != nil {
		return nil, 0, err
	}
	if len(logs) == 0 {
		return nil, 0, nil
	}

	// FetchLogsFull orders by timestamp descending; restore the on-chain order.
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].BlockNumber != logs[j].BlockNumber {
			return logs[i].BlockNumber < logs[j].BlockNumber
		}
		return logs[i].LogIndex < logs[j].LogIndex
	})

	paths := make([]cmpaths.IMachinePathManagerMachinePath, 0, len(logs))
	for i := range logs {
		ethLog, err := logs[i].ToEthLog()
		if err != nil {
			return nil, 0, fmt.Errorf("converting log: %w", err)
		}

		event, err := filterer.ParseMachinePathsAdded(ethLog)
		if err != nil {
			return nil, 0, fmt.Errorf("parsing MachinePathsAdded event: %w", err)
		}

		for _, p := range event.Paths {
			paths = append(paths, cmpaths.IMachinePathManagerMachinePath{
				SourceTeeIds:      p.SourceTeeIds,
				DestinationTeeIds: p.DestinationTeeIds,
			})
		}
	}

	return paths, logs[0].BlockNumber, nil
}

// signatures returns the governance signatures for the list (extensionID, nonce)
// from successful signMachinePathList transactions in the block range
// (fromBlock, toBlock]. The contract accepts only governance signers, so a
// successful transaction carries an accepted signature.
func (s *Source) signatures(ctx context.Context, extensionID common.Hash, nonce uint64, fromBlock, toBlock int64) ([]machinepathmanager.Signature, error) {
	txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, s.db, database.TxParams{
		ToAddress:   s.addresses.MachinePathManager,
		FunctionSel: signMachinePathListSel,
		From:        fromBlock,
		To:          toBlock,
	})
	if err != nil {
		return nil, err
	}

	extIDBig := new(big.Int).SetBytes(extensionID[:])
	nonceBig := new(big.Int).SetUint64(nonce)

	sigs := make([]machinepathmanager.Signature, 0, len(txs))
	for i := range txs {
		if txs[i].Status != txSuccessStatus {
			continue
		}

		extID, txNonce, sig, err := recoverSignMachinePathListInputs(txs[i].Input)
		if err != nil {
			continue
		}
		if extID.Cmp(extIDBig) != 0 || txNonce.Cmp(nonceBig) != 0 {
			continue
		}

		sigs = append(sigs, sig)
	}

	return sigs, nil
}

// recoverSignMachinePathListInputs unpacks the calldata of a signMachinePathList
// transaction into its extension ID, nonce, and governance signature. The field
// names match the ABI inputs (_extensionId, _nonce, _signature) so the generated
// argument set can unpack directly into the struct.
func recoverSignMachinePathListInputs(input string) (extensionID, nonce *big.Int, sig machinepathmanager.Signature, err error) {
	inputB, err := hex.DecodeString(input)
	if err != nil {
		return nil, nil, machinepathmanager.Signature{}, fmt.Errorf("decoding transaction input: %w", err)
	}
	if len(inputB) < 4 {
		return nil, nil, machinepathmanager.Signature{}, errInvalidInputs
	}

	values, err := signMachinePathListArgs.Unpack(inputB[4:])
	if err != nil {
		return nil, nil, machinepathmanager.Signature{}, err
	}

	var out struct {
		ExtensionId *big.Int
		Nonce       *big.Int
		Signature   machinepathmanager.Signature
	}
	if err := signMachinePathListArgs.Copy(&out, values); err != nil {
		return nil, nil, machinepathmanager.Signature{}, err
	}

	return out.ExtensionId, out.Nonce, out.Signature, nil
}
