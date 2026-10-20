package indexer

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
)

var (
	// signingPolicyInitializedEventSel is the topic0 of the Relay's SigningPolicyInitialized event.
	signingPolicyInitializedEventSel common.Hash
	// voterRegisteredEventSel is the topic0 of the VoterRegistry's VoterRegistered event.
	voterRegisteredEventSel common.Hash
	// signNewSigningPolicySel is the FlareSystemsManager signNewSigningPolicy selector.
	signNewSigningPolicySel [4]byte

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
	relayABI, err := relay.RelayMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("loading relay ABI: %w", err))
	}
	signingPolicyInitializedEventSel = mustEvent(relayABI, "SigningPolicyInitialized").ID

	registryABI, err := registry.RegistryMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("loading voterRegistry ABI: %w", err))
	}
	voterRegisteredEventSel = mustEvent(registryABI, "VoterRegistered").ID

	fsmABI, err := system.FlareSystemsManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("loading flareSystemsManager ABI: %w", err))
	}
	copy(signNewSigningPolicySel[:], mustMethod(fsmABI, "signNewSigningPolicy").ID)

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("loading machinePathManager ABI: %w", err))
	}
	machinePathListSignedEventSel = mustEvent(managerABI, "MachinePathListSigned").ID
	machinePathsAddedEventSel = mustEvent(managerABI, "MachinePathsAdded").ID
	machinePathListApprovedEventSel = mustEvent(managerABI, "MachinePathListApproved").ID

	signMethod := mustMethod(managerABI, "signMachinePathList")
	copy(signMachinePathListSel[:], signMethod.ID)
	signMachinePathListArgs = signMethod.Inputs

	filterer, err = machinepathmanager.NewMachinePathManagerFilterer(common.Address{}, nil)
	if err != nil {
		panic(fmt.Errorf("creating machinePathManager filterer: %w", err))
	}
}

func mustEvent(a *abi.ABI, name string) abi.Event {
	e, ok := a.Events[name]
	if !ok {
		panic(errors.New(name + " event not found in ABI"))
	}
	return e
}

func mustMethod(a *abi.ABI, name string) abi.Method {
	m, ok := a.Methods[name]
	if !ok {
		panic(errors.New(name + " method not found in ABI"))
	}
	return m
}
