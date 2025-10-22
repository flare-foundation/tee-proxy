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

type Receipt struct {
	InstructionHash               common.Hash   `json:"instructionHash"`
	Sequence                      uint64        `json:"sequence"`
	Signature                     hexutil.Bytes `json:"signature"`
	AdditionalVariableMessageHash common.Hash   `json:"additionalVariableMessageHash"`
	Timestamp                     uint64        `json:"timestamp"`
	VoteHash                      common.Hash   `json:"voteHash"`
}

func (r *Receipt) Hash() (common.Hash, error) {
	rd := tee.TeeStructsVoteReceipt{
		InstructionHash:               r.InstructionHash,
		Sequence:                      r.Sequence,
		Signature:                     r.Signature,
		AdditionalVariableMessageHash: r.AdditionalVariableMessageHash,
		Timestamp:                     r.Timestamp,
		VoteHash:                      r.VoteHash,
	}
	e, err := structs.Encode(tee.StructArg[tee.VoteReceipt], rd)
	if err != nil {
		return common.Hash{}, err
	}

	return crypto.Keccak256Hash(e), nil
}

// SignedReceipt combines Receipt and its signature.
type SignedReceipt struct {
	Receipt   Receipt       `json:"receipt"`
	Signature hexutil.Bytes `json:"signature"`
}

// Sign signs the receipt with the private key and returns signed receipt.
func (r *Receipt) Sign(sk *ecdsa.PrivateKey) (*SignedReceipt, error) {
	h, err := r.Hash()
	if err != nil {
		return nil, err
	}

	sig, err := crypto.Sign(accounts.TextHash(h[:]), sk)
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
func (sr *SignedReceipt) RecoverPubKey() (*ecdsa.PublicKey, error) {
	h, err := sr.Receipt.Hash()
	if err != nil {
		return nil, err
	}
	msg := accounts.TextHash(h.Bytes())

	return crypto.SigToPub(msg, sr.Signature)
}
