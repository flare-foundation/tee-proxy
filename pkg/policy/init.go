package policy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
)

var (
	signingPolicyInitializedEventSel common.Hash
	voterRegisteredEventSel          common.Hash

	signNewSigningPolicySel [4]byte // len = 4

	signNewSigningPolicyArgs abi.Arguments // FlareSystemsManager signNewSigningPolicy.

	msgArgs abi.Arguments // (uint32, address) (on contract it is (uint24, address) but the encoding is the same), see variable messageHash in registerVoter method.
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
