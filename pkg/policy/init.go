package policy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/entitymanager"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/preregistry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
)

var (
	signingPolicyAddressRegistrationConfirmedEventSel common.Hash
	signingPolicyInitializedEventSel                  common.Hash
	voterPreRegisteredEventSel                        common.Hash
	voterRegisteredEventSel                           common.Hash

	signNewSigningPolicySel [4]byte // len = 4

	registerVoterArgs        abi.Arguments // same as preRegisterVoterArgs
	signNewSigningPolicyArgs abi.Arguments // same as preRegisterVoterArgs

	msgArgs abi.Arguments
)

func init() {
	// flare systems manager
	flareSystemsManagerABI, err := system.FlareSystemsManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get flareSystemsManagerABI: %w", err))
	}

	signNewSigningPolicyMethod, ok := flareSystemsManagerABI.Methods["signNewSigningPolicy"]
	if !ok {
		panic(fmt.Errorf("cannot get signNewSigningPolicy method: %w", err))
	}
	copy(signNewSigningPolicySel[:], signNewSigningPolicyMethod.ID)
	signNewSigningPolicyArgs = signNewSigningPolicyMethod.Inputs

	// relay
	relayABI, err := relay.RelayMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get relayABI: %w", err))
	}

	signingPolicyEvent, ok := relayABI.Events["SigningPolicyInitialized"]
	if !ok {
		panic(fmt.Errorf("cannot get SigningPolicyInitialized event: %w", err))
	}
	signingPolicyInitializedEventSel = signingPolicyEvent.ID

	// voter registry
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

	// voter pre registry
	voterPreRegistryABI, err := preregistry.PreregistryMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get voterPreRegistryABI: %w", err))
	}

	voterPreRegisteredEvent, ok := voterPreRegistryABI.Events["VoterPreRegistered"]
	if !ok {
		panic(fmt.Errorf("cannot get VoterPreRegistered event: %w", err))
	}
	voterPreRegisteredEventSel = voterPreRegisteredEvent.ID

	// entity manager
	entityManagerABI, err := entitymanager.EntityManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get entityManagerABI: %w", err))
	}

	signingPolicyAddressRegistrationConfirmedEvent, ok := entityManagerABI.Events["SigningPolicyAddressRegistrationConfirmed"]
	if !ok {
		panic(fmt.Errorf("cannot get SigningPolicyAddressRegistrationConfirmed event: %w", err))
	}
	signingPolicyAddressRegistrationConfirmedEventSel = signingPolicyAddressRegistrationConfirmedEvent.ID

	// registration message to sign abi
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
