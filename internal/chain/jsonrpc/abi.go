package jsonrpc

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
)

var (
	signingPolicyInitializedEventSel common.Hash
	voterRegisteredEventSel          common.Hash
	signingPolicySignedEventSel      common.Hash
	signNewSigningPolicySel          []byte

	// noActiveMachinePathListSel is the revert selector of getActiveMachinePathListNonce
	// for an extension without a signed list.
	noActiveMachinePathListSel []byte

	// initialRewardEpochIdSel calls Relay.initialRewardEpochId(), which the binding lacks.
	initialRewardEpochIdSel = crypto.Keccak256([]byte("initialRewardEpochId()"))[:4]

	relayFilterer    *relay.RelayFilterer
	registryFilterer *registry.RegistryFilterer
)

func init() {
	relayABI := mustABI(relay.RelayMetaData.GetAbi())
	signingPolicyInitializedEventSel = mustEvent(relayABI, "SigningPolicyInitialized").ID

	registryABI := mustABI(registry.RegistryMetaData.GetAbi())
	voterRegisteredEventSel = mustEvent(registryABI, "VoterRegistered").ID

	fsmABI := mustABI(system.FlareSystemsManagerMetaData.GetAbi())
	signingPolicySignedEventSel = mustEvent(fsmABI, "SigningPolicySigned").ID
	signNewSigningPolicySel = mustMethod(fsmABI, "signNewSigningPolicy").ID

	managerABI := mustABI(machinepathmanager.MachinePathManagerMetaData.GetAbi())
	noActive, ok := managerABI.Errors["NoActiveMachinePathList"]
	if !ok {
		panic(errors.New("NoActiveMachinePathList error not found in ABI"))
	}
	noActiveMachinePathListSel = noActive.ID[:4]

	var err error
	relayFilterer, err = relay.NewRelayFilterer(common.Address{}, nil)
	if err != nil {
		panic(fmt.Errorf("creating relay filterer: %w", err))
	}
	registryFilterer, err = registry.NewRegistryFilterer(common.Address{}, nil)
	if err != nil {
		panic(fmt.Errorf("creating registry filterer: %w", err))
	}
}

func mustABI(a *abi.ABI, err error) *abi.ABI {
	if err != nil {
		panic(fmt.Errorf("loading ABI: %w", err))
	}
	return a
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
