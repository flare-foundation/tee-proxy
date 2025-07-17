package voting

import (
	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/tee"
)

// Receipt as used for voting transparency.
type Receipt struct {
	InstructionHash               common.Hash `json:"instructionHash"`
	Sequence                      uint64      `json:"sequence"`
	Signature                     []byte      `json:"signature"`
	AdditionalVariableMessageHash common.Hash `json:"additionalVariableMessageHash"`
	Timestamp                     uint64      `json:"timestamp"`
	VoteHash                      common.Hash `json:"voteHash"`
}

// SignedReceipt combines Receipt and its signature.
type SignedReceipt struct {
	Receipt   Receipt       `json:"receipt"`
	Signature hexutil.Bytes `json:"signature"`
}

// Hash returns the hash of the receipt.
func (r *Receipt) Hash() (common.Hash, error) {
	s := tee.TeeStructsVoteReceipt{
		InstructionHash:               r.InstructionHash,
		Sequence:                      r.Sequence,
		Signature:                     r.Signature,
		AdditionalVariableMessageHash: r.AdditionalVariableMessageHash,
		Timestamp:                     r.Timestamp,
		VoteHash:                      r.VoteHash,
	}

	e, err := structs.Encode(tee.StructArg[tee.VoteReceipt], s)
	if err != nil {
		return common.Hash{}, err
	}

	return crypto.Keccak256Hash(e), nil
}

// Sign signs the receipt with the private key and returns signed receipt.
func (r *Receipt) Sign(pk *ecdsa.PrivateKey) (*SignedReceipt, error) {
	h, err := r.Hash()
	if err != nil {
		return nil, err
	}

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
	h, err := r.Receipt.Hash()
	if err != nil {
		return nil, err
	}

	msg := accounts.TextHash(h.Bytes())

	return crypto.SigToPub(msg, r.Signature)
}
