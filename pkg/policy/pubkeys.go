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
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"gorm.io/gorm"
)

const costonRegistryMessageBreakingEpoch = 5451

var costonOldVoterRegistryAddress = common.HexToAddress("0xB4B93a3A3ADa93a574E6efeb5f295bf882934cB6")

var (
	errInvalidLogCountPubKeys = errors.New("invalid number of logs")
	errWrongAddressRecovered  = errors.New("wrong address recovered")

	costonChainID = big.NewInt(16)
)

// recoverPubKey recovers public key for signingPolicyAddress in signingPolicyID.
func recoverPubKey(
	ctx context.Context,
	db *gorm.DB,
	signingPolicyAddress common.Address,
	signingPolicyID uint32,
	voterRegistryAddress common.Address,
	chainID *big.Int,
) (*ecdsa.PublicKey, error) {
	vrLogs, err := fetchVoterRegistered(ctx, db, voterRegistryAddress, signingPolicyID, signingPolicyAddress)
	if err != nil {
		return nil, fmt.Errorf("fetching voter registered event: %w", err)
	}
	if len(vrLogs) != 1 {
		return nil, errInvalidLogCountPubKeys
	}

	pub, err := recoverPubKeyFromEvent(signingPolicyAddress, signingPolicyID, vrLogs[0], chainID)
	if err != nil {
		return nil, fmt.Errorf("recovering public key from event for %v: %w", signingPolicyAddress, err)
	}

	return pub, nil
}

// fetchVoterRegistered fetches all voterRegistered event logs emitted by voterRegistry
// with signingPolicyID and signingPolicyAddress as third and fourth topic respectively.
//
// There should always be at most one such event.
func fetchVoterRegistered(
	ctx context.Context,
	db *gorm.DB,
	voterRegistryAddress common.Address,
	signingPolicyID uint32,
	signingPolicyAddress common.Address,
) ([]database.Log, error) {
	topics := [4]common.Hash{}

	topics[0] = voterRegisteredEventSel
	topics[2] = convert.Uint32ToHash(signingPolicyID)
	topics[3] = AddressToHash(signingPolicyAddress)

	params := database.LogsFullParams{
		Address: voterRegistryAddress,
		Topics:  topics,
		Number:  -1,
	}

	return database.FetchLogsFull(ctx, db, params)
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
func recoverPubKeyFromEvent(signingPolicyAddress common.Address, signingPolicyID uint32, log database.Log, chainID *big.Int) (*ecdsa.PublicKey, error) {
	event, err := policy.ParseVoterRegisteredEvent(log)
	if err != nil {
		return nil, fmt.Errorf("parsing voter registered event: %w", err)
	}

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
