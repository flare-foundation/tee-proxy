package policy

import (
	"context"
	"crypto/ecdsa"
	"errors"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"gorm.io/gorm"
)

var (
	errInvalidLogCountPubKeys = errors.New("invalid number of logs")
	errWrongAddressRecovered  = errors.New("wrong address recovered")
)

// recoverPubKey recovers public key for signingPolicyAddress in signingPolicyID.
func recoverPubKey(
	ctx context.Context,
	db *gorm.DB,
	signingPolicyAddress common.Address,
	signingPolicyID uint32,
	voterRegistryAddress common.Address,
) (*ecdsa.PublicKey, error) {
	vrLogs, err := fetchVoterRegistered(ctx, db, voterRegistryAddress, signingPolicyID, signingPolicyAddress)
	if err != nil {
		return nil, err
	}
	if len(vrLogs) != 1 {
		return nil, errInvalidLogCountPubKeys
	}

	pub, err := recoverPubKeyFromEvent(signingPolicyAddress, signingPolicyID, vrLogs[0])
	if err != nil {
		return nil, err
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
func recoverPubKeyFromRegistration(identityAddress common.Address, rewardEpochID uint32, signature *registry.Signature) (*ecdsa.PublicKey, error) {
	msg, err := msgArgs.Pack(rewardEpochID, identityAddress)
	if err != nil {
		return nil, err
	}

	sigMsg := accounts.TextHash(crypto.Keccak256(msg))

	return crypto.SigToPub(sigMsg, serializeSig(signature))
}

// recoverPubKeyFromEvent recovers public key from VoterRegistered event.
func recoverPubKeyFromEvent(signingPolicyAddress common.Address, signingPolicyID uint32, log database.Log) (*ecdsa.PublicKey, error) {
	event, err := policy.ParseVoterRegisteredEvent(log)
	if err != nil {
		return nil, err
	}

	pub, err := recoverPubKeyFromRegistration(event.Voter, signingPolicyID, &event.Signature)
	if err != nil {
		return nil, err
	}

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	if recoveredAddress != signingPolicyAddress {
		return nil, errWrongAddressRecovered
	}

	return pub, nil
}
