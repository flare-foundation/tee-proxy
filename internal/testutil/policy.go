package testutil

import (
	"crypto/ecdsa"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
)

var TestSigningPolicy *policy.SigningPolicy

var (
	PrivKey1 *ecdsa.PrivateKey
	PrivKey2 *ecdsa.PrivateKey
	PrivKey3 *ecdsa.PrivateKey
)

func init() {
	var err error

	PrivKey1, err = crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}
	PrivKey2, err = crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}
	PrivKey3, err = crypto.GenerateKey()
	if err != nil {
		panic("cannot generate key")
	}

	voters := make([]common.Address, 0, 3)

	voters = append(voters, crypto.PubkeyToAddress(PrivKey1.PublicKey))
	voters = append(voters, crypto.PubkeyToAddress(PrivKey2.PublicKey))
	voters = append(voters, crypto.PubkeyToAddress(PrivKey3.PublicKey))

	event := relay.RelaySigningPolicyInitialized{
		RewardEpochId:      big.NewInt(1),
		StartVotingRoundId: 0,
		Threshold:          3,
		Seed:               big.NewInt(2),
		Voters:             voters,
		Weights:            []uint16{1, 3, 3},
		SigningPolicyBytes: []byte{},
		Timestamp:          0,
	}

	TestSigningPolicy = policy.NewSigningPolicy(&event, nil)
}
