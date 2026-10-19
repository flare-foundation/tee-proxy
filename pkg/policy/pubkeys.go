package policy

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-node/pkg/types"
)

const costonRegistryMessageBreakingEpoch = 5451

var costonOldVoterRegistryAddress = common.HexToAddress("0xB4B93a3A3ADa93a574E6efeb5f295bf882934cB6")

var (
	errInvalidLogCountPubKeys = errors.New("invalid number of logs")
	errWrongAddressRecovered  = errors.New("wrong address recovered")

	costonChainID = big.NewInt(16)
)

// fetchPubKeys returns the public keys of signingPolicy's voters in voter order,
// recovered from their VoterRegistered events. Each voter must have exactly one.
func fetchPubKeys(ctx context.Context, src Source, voterRegistryAddress common.Address, signingPolicy *policy.SigningPolicy, chainID *big.Int) ([]types.PublicKey, error) {
	if chainID.Cmp(costonChainID) == 0 && signingPolicy.RewardEpochID < costonRegistryMessageBreakingEpoch {
		voterRegistryAddress = costonOldVoterRegistryAddress
	}

	voters := signingPolicy.Voters.Voters()
	registrations, err := src.VoterRegistrations(ctx, voterRegistryAddress, signingPolicy.RewardEpochID, voters)
	if err != nil {
		return nil, fmt.Errorf("fetching voter registrations for epoch %d: %w", signingPolicy.RewardEpochID, err)
	}

	pubKeys := make([]types.PublicKey, len(voters))
	for j, address := range voters {
		events := registrations[address]
		if len(events) != 1 {
			return nil, fmt.Errorf("recovering pub key for %v: %w", address, errInvalidLogCountPubKeys)
		}

		pk, err := recoverPubKeyFromEvent(address, signingPolicy.RewardEpochID, events[0], chainID)
		if err != nil {
			return nil, fmt.Errorf("recovering pub key for %v: %w", address, err)
		}

		pubKeys[j] = types.PubKeyToStruct(pk)
	}

	return pubKeys, nil
}

// serializeSig serializes signature to [R||S||V-27].
func serializeSig(s *registry.Signature) []byte {
	sig := make([]byte, 0, 65)

	sig = append(sig, s.R[:]...)
	sig = append(sig, s.S[:]...)
	sig = append(sig, s.V-27)

	return sig
}

// recoverPubKeyFromRegistration recovers the public key from the signature of the message.
//
// See registerVoter method from VoterRegistry smart contract, for the message that is signed and how it is signed.
// New format: messageHash = keccak256(abi.encode(block.chainid, rewardEpochId, _voter))
// Legacy format for coston (chainID = 16): messageHash = keccak256(abi.encode(rewardEpochId, _voter))
func recoverPubKeyFromRegistration(identityAddress common.Address, rewardEpochID uint32, signature *registry.Signature, chainID *big.Int) (*ecdsa.PublicKey, error) {
	var msg []byte
	var err error

	if chainID == nil || (chainID.Cmp(costonChainID) == 0 && rewardEpochID < costonRegistryMessageBreakingEpoch) {
		msg, err = msgArgsLegacy.Pack(rewardEpochID, identityAddress)
	} else {
		msg, err = msgArgs.Pack(chainID, rewardEpochID, identityAddress)
	}
	if err != nil {
		return nil, fmt.Errorf("packing voter registry message: %w", err)
	}

	sigMsg := accounts.TextHash(crypto.Keccak256(msg))

	pubKey, err := crypto.SigToPub(sigMsg, serializeSig(signature))
	if err != nil {
		return nil, fmt.Errorf("recovering public key from signature: %w", err)
	}

	return pubKey, nil
}

// recoverPubKeyFromEvent recovers public key from VoterRegistered event.
func recoverPubKeyFromEvent(signingPolicyAddress common.Address, signingPolicyID uint32, event *registry.RegistryVoterRegistered, chainID *big.Int) (*ecdsa.PublicKey, error) {
	pub, err := recoverPubKeyFromRegistration(event.Voter, signingPolicyID, &event.Signature, chainID)
	if err != nil {
		return nil, fmt.Errorf("recovering public key from registration: %w", err)
	}

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	if recoveredAddress != signingPolicyAddress {
		return nil, errWrongAddressRecovered
	}

	return pub, nil
}
