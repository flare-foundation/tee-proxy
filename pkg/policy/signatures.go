package policy

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"gorm.io/gorm"
)

const (
	maxAllowedConsecutiveErrors = 15

	sleepBetweenSignatureQueries = 5 * time.Second
)

var (
	ErrDeadlineExceeded    = errors.New("deadline exceeded")
	ErrTooManyErrors       = errors.New("too many errors")
	ErrThresholdNotReached = errors.New("threshold not reached")

	errInvalidFirstInput    = errors.New("invalid first input")
	errInvalidSecondInput   = errors.New("invalid second input")
	errInvalidThirdInput    = errors.New("invalid third input")
	errNotConsecutivePolicy = errors.New("not consecutive policy")
	errInvalidDataProvider  = errors.New("invalid data provided")
)

// recoverInputsSignNewSigningPolicy unpacks transaction input for signNewSigningPolicy method of FlareSystemsManager smart contract.
//
// Does not work if the call to the method is done in an internal transacting.
func recoverInputsSignNewSigningPolicy(input []byte) (signingPolicyID uint32, newSigningPolicyHash common.Hash, signature *registry.Signature, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(error)
			if ok {
				err = fmt.Errorf("recovered panic: %w", e)
			} else {
				err = fmt.Errorf("recovered panic non error: %v", r)
			}
		}
	}()

	input = input[4:] // strip function selector

	inputs, err := signNewSigningPolicyArgs.Unpack(input)
	if err != nil {
		return 0, common.Hash{}, nil, err
	}

	i0 := abi.ConvertType(inputs[0], new(big.Int))

	ip0, ok := i0.(*big.Int)
	if !ok {
		return 0, common.Hash{}, nil, errInvalidFirstInput
	}

	signingPolicyID, err = convert.BigToUint32Safe(ip0)
	if err != nil {
		return 0, common.Hash{}, nil, err
	}

	i1 := abi.ConvertType(inputs[1], new(common.Hash))

	ip1, ok := i1.(*common.Hash)
	if !ok {
		return 0, common.Hash{}, nil, errInvalidSecondInput
	}

	newSigningPolicyHash = *ip1

	i2 := abi.ConvertType(inputs[2], new(registry.Signature))

	signature, ok = i2.(*registry.Signature)
	if !ok {
		return 0, common.Hash{}, nil, errInvalidThirdInput
	}

	return signingPolicyID, newSigningPolicyHash, signature, err
}

// recoverSigner returns public key form signature of a hash.
func recoverSigner(hash common.Hash, signature *registry.Signature) (*ecdsa.PublicKey, error) {
	sigMsg := accounts.TextHash(hash[:])

	return crypto.SigToPub(sigMsg, serializeSig(signature))
}

// collectSignatures collects providers' signatures for newPolicy according to the activePolicy.
// Signatures are extracted from the transactions to FlareSystemsManager calling signNewSigningPolicy method.
// The process ends when signatures of +50% weight are collected, in such case signatures are returned,
// or when deadline is exceeded or too many consecutive errors while querying db occurred, in which case error is returned.
func collectSignatures(
	ctx context.Context,
	db *gorm.DB,
	flareSystemsManagerAddress common.Address,
	startBlock int64,
	deadline uint64,
	newPolicy *policy.SigningPolicy,
	activePolicy *policy.SigningPolicy,
) ([]*registry.Signature, error) {
	if newPolicy.RewardEpochID != activePolicy.RewardEpochID+1 {
		return nil, errNotConsecutivePolicy
	}

	from := startBlock

	voters := activePolicy.Voters.Voters()
	expectedHash := common.BytesToHash(newPolicy.Hash())
	weightCollected := uint16(0)
	sigs := make([]*registry.Signature, 0, len(voters))

	voted := make(map[common.Address]bool, len(voters))

	errCount := 0

mainLoop:
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		state, err := database.FetchState(ctx, db, nil)
		if err != nil {
			errCount++

			if errCount < maxAllowedConsecutiveErrors {
				continue
			}

			return nil, fmt.Errorf("%w: last error: %w", ErrTooManyErrors, err)
		}

		if state.BlockTimestamp > deadline {
			return nil, ErrDeadlineExceeded
		}

		to := int64(state.Index)

		params := database.TxParams{
			ToAddress:   flareSystemsManagerAddress,
			FunctionSel: signNewSigningPolicySel,
			From:        from,
			To:          to,
		}

		txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, db, params)
		if err != nil {
			errCount++

			if errCount < maxAllowedConsecutiveErrors {
				continue
			}

			return nil, fmt.Errorf("%w: last error: %w", ErrTooManyErrors, err)
		}

		if len(txs) > 0 {
			for j := range txs {
				signer, sig, err := checkAndExtract(txs[j].Input, expectedHash, newPolicy.RewardEpochID)
				if err != nil {
					continue
				}

				addr := crypto.PubkeyToAddress(*signer)

				weight := activePolicy.Voters.VoterWeightForAddress(addr)

				if weight > 0 && !voted[addr] {
					sigs = append(sigs, sig)
					voted[addr] = true
					weightCollected += weight

					if weightCollected > activePolicy.Threshold {
						break mainLoop
					}
				}
			}
		}

		from = to
		errCount = 0
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleepBetweenSignatureQueries):
		}
	}

	return sigs, nil
}

// checkAndExtract recovers data (signingPolicyID, signingPolicyHash and signature) from the input and checks that they match the expectations.
// The function returns the address of the signer and the signature if the expectations are met.
func checkAndExtract(input string, expectedHash common.Hash, expectedRewardEpochID uint32) (*ecdsa.PublicKey, *registry.Signature, error) {
	inputB, err := hex.DecodeString(input)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding transaction input: %w", err)
	}
	spID, hash, sig, err := recoverInputsSignNewSigningPolicy(inputB)
	if err != nil {
		return nil, nil, fmt.Errorf("recovering signing policy inputs: %w", err)
	}
	if spID != expectedRewardEpochID || hash.Cmp(expectedHash) != 0 {
		return nil, nil, errInvalidDataProvider
	}

	signer, err := recoverSigner(expectedHash, sig)
	if err != nil {
		return nil, nil, fmt.Errorf("recovering signer: %w", err)
	}

	return signer, sig, nil
}
