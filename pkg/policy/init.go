package policy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/entitymanager"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/preregistry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
)

var (
	signingPolicyAddressRegistrationConfirmedEventSel common.Hash
	signingPolicySignedEventSel                       common.Hash
	signingPolicyInitializedEventSel                  common.Hash
	voterPreRegisteredEventSel                        common.Hash
	voterRegisteredEventSel                           common.Hash

	registerVoterArgs abi.Arguments // same as preRegisterVoterArgs

	msgArgs abi.Arguments
)

func init() {
	relayABI, err := relay.RelayMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get relayABI: %w", err))
	}

	signingPolicyEvent, ok := relayABI.Events["SigningPolicyInitialized"]
	if !ok {
		panic(fmt.Errorf("cannot get SigningPolicyInitialized event: %w", err))
	}
	signingPolicyInitializedEventSel = signingPolicyEvent.ID

	voterRegistryABI, err := registry.RegistryMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get voterRegistryABI: %w", err))
	}

	voterRegisteredEvent, ok := voterRegistryABI.Events["VoterRegistered"]
	if !ok {
		panic(fmt.Errorf("cannot get VoterRegistered event: %w", err))
	}

	voterRegisteredEventSel = voterRegisteredEvent.ID

	registerVoterMethod, ok := voterRegistryABI.Methods["registerVoter"]
	if !ok {
		panic(fmt.Errorf("cannot get registerVoter method: %w", err))
	}

	registerVoterArgs = registerVoterMethod.Inputs

	voterPreRegistryABI, err := preregistry.PreregistryMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get voterPreRegistryABI: %w", err))
	}

	voterPreRegisteredEvent, ok := voterPreRegistryABI.Events["VoterPreRegistered"]
	if !ok {
		panic(fmt.Errorf("cannot get VoterPreRegistered event: %w", err))
	}

	voterPreRegisteredEventSel = voterPreRegisteredEvent.ID

	entityManagerABI, err := entitymanager.EntityManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get entityManagerABI: %w", err))
	}

	signingPolicyAddressRegistrationConfirmedEvent, ok := entityManagerABI.Events["SigningPolicyAddressRegistrationConfirmed"]
	if !ok {
		panic(fmt.Errorf("cannot get SigningPolicyAddressRegistrationConfirmed event: %w", err))
	}

	signingPolicyAddressRegistrationConfirmedEventSel = signingPolicyAddressRegistrationConfirmedEvent.ID

	addressTy, err := abi.NewType("address", "address", nil)
	if err != nil {
		panic(fmt.Errorf("invalid address type: %w", err))
	}
	uint32Ty, err := abi.NewType("uint32", "uint32", nil)
	if err != nil {
		panic(fmt.Errorf("invalid uint32 type: %w", err))
	}

	msgArgs = abi.Arguments{
		{
			Type: uint32Ty,
		},
		{
			Type: addressTy,
		},
	}
}
