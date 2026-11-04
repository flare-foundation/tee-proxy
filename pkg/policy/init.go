package policy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

var (
	// msgArgs is for the new VoterRegistry format: messageHash = keccak256(abi.encode(block.chainid, rewardEpochId, _voter))
	msgArgs abi.Arguments
	// msgArgsLegacy is for the old VoterRegistry format (without chainId): messageHash = keccak256(abi.encode(rewardEpochId, _voter))
	// TODO: Remove msgArgsLegacy once Coston is upgraded to the new VoterRegistry contract.
	msgArgsLegacy abi.Arguments
)

func init() {
	// registration message to sign abi
	// See registerVoter method from VoterRegistry smart contract:
	// messageHash = keccak256(abi.encode(block.chainid, rewardEpochId, _voter))
	addressTy, err := abi.NewType("address", "address", nil)
	if err != nil {
		panic(fmt.Errorf("invalid address type: %w", err))
	}
	uint32Ty, err := abi.NewType("uint32", "uint32", nil)
	if err != nil {
		panic(fmt.Errorf("invalid uint32 type: %w", err))
	}
	uint256Ty, err := abi.NewType("uint256", "uint256", nil)
	if err != nil {
		panic(fmt.Errorf("invalid uint256 type: %w", err))
	}

	msgArgs = abi.Arguments{
		{
			Type: uint256Ty,
		},
		{
			Type: uint32Ty,
		},
		{
			Type: addressTy,
		},
	}

	// TODO: Remove msgArgsLegacy once Coston is upgraded to the new VoterRegistry contract.
	msgArgsLegacy = abi.Arguments{
		{
			Type: uint32Ty,
		},
		{
			Type: addressTy,
		},
	}
}
