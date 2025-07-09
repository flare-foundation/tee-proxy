package policy

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/policy"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"gorm.io/gorm"
)

// addresses stores addresses of the smart contracts.
type addresses struct {
	voterRegistry    common.Address
	voterPreRegistry common.Address
	entityManager    common.Address
}

// RecoverPubKey recovers public key for signingPolicyAddress in signingPolicyID.
func RecoverPubKey(
	ctx context.Context,
	db *gorm.DB,
	signingPolicyAddress common.Address,
	signingPolicyID uint32,
	addresses addresses,
) (*ecdsa.PublicKey, error) {
	vrLogs, err := fetchVoterRegistered(ctx, db, addresses.voterRegistry, signingPolicyID, signingPolicyAddress)
	if err != nil {
		return nil, err
	}
	if len(vrLogs) != 1 {
		return nil, errors.New("invalid number of logs")
	}

	pub, err := recoverPubKeyFromEvent(signingPolicyAddress, signingPolicyID, vrLogs[0])
	if err == nil { // return if pub is recovered
		return pub, nil
	}

	event, err := policy.ParseVoterRegisteredEvent(vrLogs[0])
	if err != nil {
		return nil, err
	}

	vprLogs, err := fetchVoterPreRegistered(ctx, db, addresses.voterPreRegistry, signingPolicyID, event.Voter)
	if err != nil || len(vprLogs) != 1 {
		goto txRecovery
	}

	pub, err = recoverPubKeyFromEvent(signingPolicyAddress, signingPolicyID, vrLogs[0])
	if err == nil { // return if pub is recovered
		return pub, nil
	}

txRecovery:
	rLog, err := fetchSigningPolicyAddressRegistrationConfirmed(ctx, db, addresses.entityManager, event.Voter, signingPolicyAddress)
	if err != nil {
		return nil, err
	}

	if len(rLog) == 0 {
		return nil, errors.New("no SigningPolicyAddressRegistrationConfirmed logs")
	}

	return recoverPubKeyFromTransaction(*rLog[0].Transaction, signingPolicyAddress)
}

// fetchSigningPolicyInitializedEventLogs fetches all signingPolicyInitialized event logs
// emitted by Relay with signingPolicyID higher or equal to initialSigningPolicyID.
func FetchSigningPolicyInitializedEvents(
	ctx context.Context,
	db *gorm.DB,
	relayAddress common.Address,
	initialSigningPolicyID uint32,
) ([]database.Log, error) {
	var logs []database.Log

	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 >= ?",
		AddressToQueryParam(relayAddress),
		hex.EncodeToString(signingPolicyInitializedEventSel[:]),
		hex.EncodeToString(Uint32ToHash(initialSigningPolicyID).Bytes()),
	).Order("timestamp").Find(&logs).Error // todo add retry

	return logs, err
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
	topics[2] = Uint32ToHash(signingPolicyID)
	topics[3] = AddressToHash(signingPolicyAddress)

	params := database.LogsFullParams{
		Address: voterRegistryAddress,
		Topics:  topics,
		Number:  -1,
	}

	return database.FetchLogsFull(ctx, db, params)
}

// fetchVoterPreRegistered fetches all voterPreRegistered event logs emitted by voterPreRegistry
// with identityAddress and signingPolicyID as second and third topic respectively.
//
// There should always be at most one such event.
func fetchVoterPreRegistered(
	ctx context.Context,
	db *gorm.DB,
	voterPreRegistryAddress common.Address,
	signingPolicyID uint32,
	identityAddress common.Address,
) ([]database.Log, error) {
	topics := [4]common.Hash{}

	topics[0] = voterPreRegisteredEventSel
	topics[1] = AddressToHash(identityAddress)
	topics[2] = Uint32ToHash(signingPolicyID)

	params := database.LogsFullParams{
		Address: voterPreRegistryAddress,
		Topics:  topics,
		Number:  -1,
	}

	return database.FetchLogsFull(ctx, db, params)
}

func fetchSigningPolicyAddressRegistrationConfirmed(
	ctx context.Context,
	db *gorm.DB,
	entityManagerAddress common.Address,
	identityAddress,
	signingPolicyAddress common.Address,
) ([]database.Log, error) {
	topics := [4]common.Hash{}

	topics[0] = signingPolicyAddressRegistrationConfirmedEventSel
	topics[1] = AddressToHash(identityAddress)
	topics[2] = AddressToHash(signingPolicyAddress)

	params := database.LogsFullParams{
		Address: entityManagerAddress,
		Topics:  topics,
		Number:  -1,
	}

	return database.FetchLogsFull(ctx, db, params)
}

// recoverInputsRegisterVoter unpacks arguments for registerVoter of preRegisterVoter method.
func recoverInputsRegisterVoter(input []byte) (identityAddress common.Address, sig *registry.IVoterRegistrySignature, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(error)
			if ok {
				err = fmt.Errorf("recovered panic: %w", e)
			} else {
				err = fmt.Errorf("recovered panic non error: %w", e)
			}
		}
	}()

	input = input[4:] // strip function selector

	inputs, err := registerVoterArgs.Unpack(input)
	if err != nil {
		return common.Address{}, nil, err
	}

	identityAddress, ok := inputs[0].(common.Address)
	if !ok {
		return common.Address{}, nil, errors.New("invalid first input")
	}

	s := abi.ConvertType(inputs[1], new(registry.IVoterRegistrySignature))

	sig, ok = s.(*registry.IVoterRegistrySignature)
	if !ok {
		return common.Address{}, nil, errors.New("invalid second input")
	}

	return identityAddress, sig, nil
}

// serializeSig serializes signature to [R||S||V-27].
func serializeSig(s *registry.IVoterRegistrySignature) []byte {
	sig := make([]byte, 0, 65)

	sig = append(sig, s.R[:]...)
	sig = append(sig, s.S[:]...)
	sig = append(sig, s.V-27)

	return sig
}

// recoverPubKeyFromRegistration recovers the public key from the signature of the message.
//
// See preRegisterVoter or registerVoter of VoterPreRegistry or VoterRegistry, for the message that is signed and how it is signed.
func recoverPubKeyFromRegistration(identityAddress common.Address, rewardEpochID uint32, signature *registry.IVoterRegistrySignature) (*ecdsa.PublicKey, error) {
	msg, err := msgArgs.Pack(rewardEpochID, identityAddress)
	if err != nil {
		return nil, err
	}

	sigMsg := accounts.TextHash(crypto.Keccak256(msg))

	return crypto.SigToPub(sigMsg, serializeSig(signature))
}

// recoverPubKeyFromEvent recovers public key from the input of a transaction that emitted log.
// The log should be VoterRegistered or VoterPreRegistered.
// If the underling transaction should call registerVoter or preRegisterVoter method.
func recoverPubKeyFromEvent(signingPolicyAddress common.Address, signingPolicyID uint32, log database.Log) (*ecdsa.PublicKey, error) {
	input, err := hex.DecodeString(log.Transaction.Input)
	if err != nil {
		return nil, fmt.Errorf("invalid tx input: %w", err)
	}

	identityAddress, sig, err := recoverInputsRegisterVoter(input)
	if err != nil {
		return nil, fmt.Errorf("invalid tx input format: %w", err)
	}

	pub, err := recoverPubKeyFromRegistration(identityAddress, signingPolicyID, sig)
	if err != nil {
		return nil, err
	}

	recoveredAddress := crypto.PubkeyToAddress(*pub)
	if recoveredAddress != signingPolicyAddress {
		return nil, errors.New("wrong address recovered")
	}

	return pub, nil
}

func recoverPubKeyFromTransaction(tx database.Transaction, signingPolicyAddress common.Address) (*ecdsa.PublicKey, error) {
	return nil, errors.New("todo")
}
