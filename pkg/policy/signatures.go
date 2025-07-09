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
func collectSignatures(ctx context.Context, db *gorm.DB, flareSystemsManagerAddress common.Address, startBlock int64, deadline uint64, newPolicy policy.SigningPolicy, activePolicy policy.SigningPolicy) ([]*registry.IVoterRegistrySignature, error) {
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
				input, err := hex.DecodeString(txs[j].Input)
				if err != nil {
					continue
				}
				spID, hash, sig, err := recoverInputsSignNewSigningPolicy(input)
				if err != nil {
					continue
				}
				if int64(spID) != newPolicy.RewardEpochID || hash.Cmp(expectedHash) != 0 {
					continue
				}

				signer, err := recoverSigner(expectedHash, sig)
				if err != nil {
					continue
				}

				signerAddress := crypto.PubkeyToAddress(*signer)
				weight := activePolicy.Voters.VoterWeightForAddress(signerAddress)

				if !voted[signerAddress] {
					sigs = append(sigs, sig)
					voted[signerAddress] = true
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
