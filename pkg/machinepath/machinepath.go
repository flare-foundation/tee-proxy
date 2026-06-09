// Package machinepath assembles SET_MACHINE_PATH_LIST direct actions for the
// TEE node from governance-signed machine path lists recorded on chain by the
// MachinePathManager facet and indexed by the C-chain indexer.
package machinepath

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"gorm.io/gorm"
)

const (
	txSuccessStatus = 1

	// recentSignedLogsLimit bounds the LatestSignedList query. It must cover
	// any out-of-order signing window: as long as no more than this many
	// lists are signed between the highest nonce being submitted and a later
	// query, the highest nonce > lastNonce will be among the results.
	recentSignedLogsLimit = 20
)

var (
	errNoPaths       = errors.New("no machine paths found for list")
	errNoSignatures  = errors.New("no governance signatures found for list")
	errInvalidInputs = errors.New("invalid signMachinePathList inputs")
)

// SetMachinePathListAction builds the SET_MACHINE_PATH_LIST direct action for
// the governance-signed list identified by (extensionID, nonce). It reconstructs
// the canonical path order from MachinePathsAdded events and collects the
// governance signatures from successful signMachinePathList transactions up to
// toBlock. The TEE node re-verifies the signatures against its own governance
// set and threshold.
func SetMachinePathListAction(
	ctx context.Context,
	db *gorm.DB,
	managerAddress common.Address,
	extensionID common.Hash,
	chainID uint64,
	nonce uint64,
	toBlock uint64,
) (*types.Action, error) {
	paths, fromBlock, err := fetchPaths(ctx, db, managerAddress, extensionID, nonce)
	if err != nil {
		return nil, fmt.Errorf("fetching machine paths: %w", err)
	}

	dataHash, err := types.MachinePathListDataHash(extensionID, nonce, paths)
	if err != nil {
		return nil, fmt.Errorf("computing machine path list data hash: %w", err)
	}
	hash, err := csigning.NewPayload(csigning.TEEMachinePathList, chainID, dataHash).Hash()
	if err != nil {
		return nil, fmt.Errorf("computing machine path list message hash: %w", err)
	}

	sigs, err := collectSignatures(ctx, db, managerAddress, extensionID, nonce, hash, int64(fromBlock)-1, int64(toBlock))
	if err != nil {
		return nil, fmt.Errorf("collecting governance signatures: %w", err)
	}

	req := types.SetMachinePathListRequest{
		Paths:      paths,
		Nonce:      nonce,
		Signatures: sigs,
	}

	msg, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling set machine path list request: %w", err)
	}

	return queue.PrepareDirectAction(op.Governance, op.SetMachinePathList, msg)
}

// LatestSignedList returns the highest nonce among MachinePathListSigned events
// for extensionID that is strictly greater than afterNonce, together with the
// block in which that list was signed. found is false when no newer signed list
// exists.
func LatestSignedList(
	ctx context.Context,
	db *gorm.DB,
	managerAddress common.Address,
	extensionID common.Hash,
	afterNonce uint64,
) (nonce uint64, toBlock uint64, found bool, err error) {
	topics := [4]common.Hash{}
	topics[0] = MachinePathListSignedEventSel
	topics[1] = extensionID

	logs, err := database.FetchLogsFull(ctx, db, database.LogsFullParams{
		Address: managerAddress,
		Topics:  topics,
		Number:  recentSignedLogsLimit,
	})
	if err != nil {
		return 0, 0, false, err
	}

	for i := range logs {
		ethLog, err := logs[i].ToEthLog()
		if err != nil {
			return 0, 0, false, fmt.Errorf("converting log: %w", err)
		}

		event, err := filterer.ParseMachinePathListSigned(ethLog)
		if err != nil {
			return 0, 0, false, fmt.Errorf("parsing MachinePathListSigned event: %w", err)
		}
		if !event.Nonce.IsUint64() {
			continue
		}

		n := event.Nonce.Uint64()
		if n > afterNonce && (!found || n > nonce) {
			nonce = n
			toBlock = logs[i].BlockNumber
			found = true
		}
	}

	return nonce, toBlock, found, nil
}

// fetchPaths returns the machine paths of the list (extensionID, nonce) in the
// order they were added on chain, together with the earliest block in which a
// path was added. The order matches the on-chain message hash construction.
func fetchPaths(
	ctx context.Context,
	db *gorm.DB,
	managerAddress common.Address,
	extensionID common.Hash,
	nonce uint64,
) ([]cmpaths.IMachinePathManagerMachinePath, uint64, error) {
	topics := [4]common.Hash{}
	topics[0] = machinePathsAddedEventSel
	topics[1] = extensionID
	topics[2] = convert.Uint64ToHash(nonce)

	logs, err := database.FetchLogsFull(ctx, db, database.LogsFullParams{
		Address: managerAddress,
		Topics:  topics,
		Number:  -1,
	})
	if err != nil {
		return nil, 0, err
	}
	if len(logs) == 0 {
		return nil, 0, errNoPaths
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

// collectSignatures gathers the governance signatures for the list
// (extensionID, nonce) from successful signMachinePathList transactions in the
// block range (fromBlock, toBlock]. Each signature is recovered against hash to
// deduplicate signers; only transactions that succeeded on chain are
// considered, so every recovered signer is a valid governance signer.
func collectSignatures(
	ctx context.Context,
	db *gorm.DB,
	managerAddress common.Address,
	extensionID common.Hash,
	nonce uint64,
	hash common.Hash,
	fromBlock, toBlock int64,
) ([][]byte, error) {
	txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, db, database.TxParams{
		ToAddress:   managerAddress,
		FunctionSel: signMachinePathListSel,
		From:        fromBlock,
		To:          toBlock,
	})
	if err != nil {
		return nil, err
	}

	extIDBig := new(big.Int).SetBytes(extensionID[:])
	nonceBig := new(big.Int).SetUint64(nonce)

	sigs := make([][]byte, 0, len(txs))
	seen := make(map[common.Address]bool, len(txs))

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

		serialized := serializeSig(sig)
		signer, err := teeutils.SignatureToSignersAddress(hash[:], serialized)
		if err != nil {
			continue
		}
		if seen[signer] {
			continue
		}

		seen[signer] = true
		sigs = append(sigs, serialized)
	}

	if len(sigs) == 0 {
		return nil, errNoSignatures
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

// serializeSig serializes a contract Signature to [R||S||V-27].
func serializeSig(s machinepathmanager.Signature) []byte {
	sig := make([]byte, 0, 65)
	sig = append(sig, s.R[:]...)
	sig = append(sig, s.S[:]...)
	sig = append(sig, s.V-27)

	return sig
}
