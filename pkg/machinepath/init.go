package machinepath

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
)

var (
	// MachinePathListSignedEventSel is the topic0 of the MachinePathManager
	// MachinePathListSigned event, emitted once a list reaches the on-chain
	// governance threshold.
	MachinePathListSignedEventSel common.Hash
	// machinePathsAddedEventSel is the topic0 of the MachinePathsAdded event,
	// which carries the paths appended to a list.
	machinePathsAddedEventSel common.Hash

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
	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("loading machinePathManager ABI: %w", err))
	}

	signedEvent, ok := managerABI.Events["MachinePathListSigned"]
	if !ok {
		panic(errors.New("MachinePathListSigned event not found in machinePathManager ABI"))
	}
	MachinePathListSignedEventSel = signedEvent.ID

	addedEvent, ok := managerABI.Events["MachinePathsAdded"]
	if !ok {
		panic(errors.New("MachinePathsAdded event not found in machinePathManager ABI"))
	}
	machinePathsAddedEventSel = addedEvent.ID

	signMethod, ok := managerABI.Methods["signMachinePathList"]
	if !ok {
		panic(errors.New("signMachinePathList method not found in machinePathManager ABI"))
	}
	copy(signMachinePathListSel[:], signMethod.ID)
	signMachinePathListArgs = signMethod.Inputs

	filterer, err = machinepathmanager.NewMachinePathManagerFilterer(common.Address{}, nil)
	if err != nil {
		panic(fmt.Errorf("creating machinePathManager filterer: %w", err))
	}
}
