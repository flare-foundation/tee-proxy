package indexer

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"

	"github.com/flare-foundation/tee-proxy/internal/chain"
)

var (
	// machinePathListSignedEventSel is the topic0 of the MachinePathManager
	// MachinePathListSigned event, emitted once a list reaches the on-chain
	// governance threshold.
	machinePathListSignedEventSel common.Hash
	// machinePathsAddedEventSel is the topic0 of the MachinePathsAdded event,
	// which carries the paths appended to a list.
	machinePathsAddedEventSel common.Hash
	// machinePathListApprovedEventSel is the topic0 of the
	// MachinePathListApproved event, emitted each time a governance Safe
	// approves a list via approveMachinePathList. It pinpoints the Safe
	// execTransaction that carries the owner signatures.
	machinePathListApprovedEventSel common.Hash

	// signMachinePathListSel is the 4-byte selector of the signMachinePathList
	// method, whose calldata carries a single governance signature.
	signMachinePathListSel [4]byte

	// signMachinePathListArgs are the inputs of the signMachinePathList method:
	// (uint256 extensionId, uint256 nonce, (uint8,bytes32,bytes32) signature).
	signMachinePathListArgs abi.Arguments

	// filterer unpacks MachinePathManager event logs. It is bound with a nil
	// backend because only ABI-based log unpacking is used, never on-chain
	// calls or filtering.
	filterer *machinepathmanager.MachinePathManagerFilterer
)

func init() {
	managerABI := chain.MustABI(machinepathmanager.MachinePathManagerMetaData)
	machinePathListSignedEventSel = chain.MustEvent(managerABI, "MachinePathListSigned").ID
	machinePathsAddedEventSel = chain.MustEvent(managerABI, "MachinePathsAdded").ID
	machinePathListApprovedEventSel = chain.MustEvent(managerABI, "MachinePathListApproved").ID

	signMethod := chain.MustMethod(managerABI, "signMachinePathList")
	copy(signMachinePathListSel[:], signMethod.ID)
	signMachinePathListArgs = signMethod.Inputs

	var err error
	filterer, err = machinepathmanager.NewMachinePathManagerFilterer(common.Address{}, nil)
	if err != nil {
		panic(fmt.Errorf("creating machinePathManager filterer: %w", err))
	}
}
