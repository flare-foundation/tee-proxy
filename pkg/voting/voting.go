package voting

import (
	"crypto/ecdsa"
	"time"

	"github.com/ethereum/go-ethereum/accounts"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/tee"
)

type Status struct {
	InstructionHash    common.Hash `json:"instructionHash"`
	Finalized          bool        `json:"finalized"`
	Deleted            bool        `json:"deleted"`
	Start              uint64      `json:"start"`
	End                uint64      `json:"end"`
	Weight             uint16      `json:"weight"`
	Threshold          uint16      `json:"threshold"`
	Cosigners          uint16      `json:"cosigners"`
	CosignersThreshold uint16      `json:"cosignersThreshold"`
}

type Statuses struct {
	InstructionID common.Hash `json:"instructionId"`
	Status        []Status    `json:"status"`
}

// SignedReceipt combines Receipt and its signature.
type SignedReceipt struct {
	Receipt   tee.TeeStructsVoteReceipt `json:"receipt"`
	Signature hexutil.Bytes             `json:"signature"`
}

// Todo: Should we just move this to go flare common?
func HashReceipt(r *tee.TeeStructsVoteReceipt) common.Hash {
	e, err := structs.Encode(tee.StructArg[tee.VoteReceipt], r)
	if err != nil {
		return common.Hash{}
	}

	return crypto.Keccak256Hash(e)
}

// Sign signs the receipt with the private key and returns signed receipt.
func SignReceipt(pk *ecdsa.PrivateKey, r *tee.TeeStructsVoteReceipt) (*SignedReceipt, error) {
	h := HashReceipt(r)

	sig, err := crypto.Sign(accounts.TextHash(h[:]), pk)
	if err != nil {
		return nil, err
	}

	sr := &SignedReceipt{
		Receipt:   *r,
		Signature: sig,
	}

	return sr, nil
}

// RecoverPubKey recovers signer of the signed receipt.
func (r *SignedReceipt) RecoverPubKey() (*ecdsa.PublicKey, error) {
	msg := accounts.TextHash(HashReceipt(&r.Receipt).Bytes())

	return crypto.SigToPub(msg, r.Signature)
}

type Config struct {
	ProposalExpiration time.Duration `toml:"proposal_expiration"` // if not positive, it defaults to 120s
	MaxPendingRequests uint          `toml:"max_pending_request"` // if not positive, it defaults to 100.
}

// setDefault sets default values if applicable.
func (v *Config) SetDefault() *Config {
	if v == nil {
		v = new(Config)
	}

	if v.MaxPendingRequests < 1 {
		v.MaxPendingRequests = 100
	}
	if v.ProposalExpiration < 1 {
		v.ProposalExpiration = 120 * time.Second
	}

	return v
}
