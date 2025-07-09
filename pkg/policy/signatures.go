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
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"gorm.io/gorm"
)

const maxAllowedConsecutiveErrors = 15

var ErrDeadlineExceeded = errors.New("deadline exceeded")
var ErrTooManyErrors = errors.New("too many errors")
var ErrThresholdNotReached = errors.New("threshold not reached")

// recoverInputsSignNewSigningPolicy unpacks transaction input for signNewSigningPolicy method of FlareSystemsManager smart contract.
//
// Does not work if the call to the method is done in an internal transacting.
func recoverInputsSignNewSigningPolicy(input []byte) (signingPolicyID uint32, newSigningPolicyHash common.Hash, signature *registry.IVoterRegistrySignature, err error) {
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

	inputs, err := signNewSigningPolicyArgs.Unpack(input)
	if err != nil {
		return 0, common.Hash{}, nil, err
	}

	i0 := abi.ConvertType(inputs[0], new(big.Int))

	ip0, ok := i0.(*big.Int)
	if !ok {
		return 0, common.Hash{}, nil, errors.New("invalid first input")
	}

	signingPolicyID, err = SafeUint32(ip0)
	if err != nil {
		return 0, common.Hash{}, nil, err
	}

	i1 := abi.ConvertType(inputs[1], new(common.Hash))

	ip1, ok := i1.(*common.Hash)
	if !ok {
		return 0, common.Hash{}, nil, errors.New("invalid second input")
	}

	newSigningPolicyHash = *ip1

	i2 := abi.ConvertType(inputs[2], new(registry.IVoterRegistrySignature))

	signature, ok = i2.(*registry.IVoterRegistrySignature)
	if !ok {
		return 0, common.Hash{}, nil, errors.New("invalid second input")
	}

	return signingPolicyID, newSigningPolicyHash, signature, err
}

// recoverSigner returns public key form signature of a hash.
func recoverSigner(hash common.Hash, signature *registry.IVoterRegistrySignature) (*ecdsa.PublicKey, error) {
	sigMsg := accounts.TextHash(hash[:])

	return crypto.SigToPub(sigMsg, serializeSig(signature))
}

// collectSignatures collects providers' signatures for newPolicy according to the activePolicy.
// Signatures are extracted from the transactions to FlareSystemsManager calling signNewSigningPolicy method.
// The process ends when signatures of +50% weight are collected, in such case signatures are returned,
// or deadline is exceeded or too many consecutive errors while querying db occurred, in which case error is returned.
func collectSignatures(
	ctx context.Context,
	db *gorm.DB,
	flareSystemsManagerAddress common.Address,
	startBlock int64,
	deadline uint64,
	newPolicy policy.SigningPolicy,
	activePolicy policy.SigningPolicy,
) ([]*registry.IVoterRegistrySignature, error) {
	from := startBlock

	expectedHash := common.BytesToHash(newPolicy.Hash())
	weightCollected := uint16(0)
	sigs := make([]*registry.IVoterRegistrySignature, 0, 100)

	voted := make(map[common.Address]bool, 100)

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

				weight := activePolicy.Voters.VoterWeightForAddress(signer)

				if weight > 0 && !voted[signer] {
					sigs = append(sigs, sig)
					voted[signer] = true
					weightCollected += weight

					if weightCollected > activePolicy.Threshold {
						break mainLoop
					}
				}
			}
		}

		from = to
		errCount = 0
		time.Sleep(5 * time.Second)
	}

	return sigs, nil
}

// fetchSignatures fetches providers' signatures for signing policy according to the previous signing policy.
// Signatures are extracted from the transactions to FlareSystemsManager calling signNewSigningPolicy method from block range [fromBlock, toBlock).
//
// The function can be used when enough signatures is already published on chain.
// Range should be set according to the signing policy - from block in which the signingPolicyInitialized is emitted to start of the signing policy.
func fetchSignatures(
	ctx context.Context,
	db *gorm.DB,
	flareSystemsManagerAddress common.Address,
	fromBlock int64,
	toBlock int64,
	sPolicy policy.SigningPolicy,
	previousSPolicy policy.SigningPolicy,
) ([]*registry.IVoterRegistrySignature, error) {
	params := database.TxParams{
		ToAddress:   flareSystemsManagerAddress,
		FunctionSel: signNewSigningPolicySel,
		From:        fromBlock,
		To:          toBlock,
	}

	expectedHash := common.BytesToHash(sPolicy.Hash())
	weightCollected := uint16(0)
	sigs := make([]*registry.IVoterRegistrySignature, 0, 100)

	voted := make(map[common.Address]bool, 100)

	txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, db, params)
	if err != nil {
		return nil, err
	}

	for j := range txs {
		signer, sig, err := checkAndExtract(txs[j].Input, expectedHash, sPolicy.RewardEpochID)
		if err != nil {
			continue
		}
		weight := previousSPolicy.Voters.VoterWeightForAddress(signer)

		if weight > 0 && !voted[signer] {
			sigs = append(sigs, sig)
			voted[signer] = true
			weightCollected += weight

			if weightCollected > previousSPolicy.Threshold {
				break
			}
		}
	}

	if weightCollected <= previousSPolicy.Threshold {
		return nil, ErrThresholdNotReached
	}

	return sigs, nil
}

// checkAndExtract recovers data (signingPolicyID, signingPolicyHash and signature) from the input and checks that they match the expectations.
// The function returns the address of the signer and the signature if the expectations are met.
func checkAndExtract(input string, expectedHash common.Hash, expectedRewardEpochID int64) (common.Address, *registry.IVoterRegistrySignature, error) {
	inputB, err := hex.DecodeString(input)
	if err != nil {
		return common.Address{}, nil, err
	}
	spID, hash, sig, err := recoverInputsSignNewSigningPolicy(inputB)
	if err != nil {
		return common.Address{}, nil, err
	}
	if int64(spID) != expectedRewardEpochID || hash.Cmp(expectedHash) != 0 {
		return common.Address{}, nil, fmt.Errorf("invalid data provided")
	}

	signer, err := recoverSigner(expectedHash, sig)
	if err != nil {
		return common.Address{}, nil, err
	}

	return crypto.PubkeyToAddress(*signer), sig, nil
}
