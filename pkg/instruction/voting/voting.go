package voting

import (
	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/accounts"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/tee"
)

// Status holds the voting state for a single instruction hash within a round.
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

// Statuses aggregates all voting statuses for a given instruction ID within a reward epoch.
type Statuses struct {
	InstructionID common.Hash `json:"instructionId"`
	FinalizedHash common.Hash `json:"finalizedHash"`
	Status        []Status    `json:"status"`
}

// Receipt is the vote receipt returned to a voter after a valid instruction is accepted.
type Receipt struct {
	InstructionHash               common.Hash   `json:"instructionHash"`
	Sequence                      uint64        `json:"sequence"`
	Signature                     hexutil.Bytes `json:"signature"`
	AdditionalVariableMessageHash common.Hash   `json:"additionalVariableMessageHash"`
	Timestamp                     uint64        `json:"timestamp"`
	VoteHash                      common.Hash   `json:"voteHash"`
}

// Hash computes the ABI-encoded Keccak256 hash of the receipt fields.
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

// SignHash returns the domain-separated, chain-bound preimage signed over a
// receipt: signing.Payload{csigning.ProxyVoteReceipt, chainID, Hash()}.Hash().
func (r *Receipt) SignHash(chainID uint64) ([32]byte, error) {
	h, err := r.Hash()
	if err != nil {
		return [32]byte{}, err
	}

	return csigning.NewPayload(csigning.ProxyVoteReceipt, chainID, h).Hash()
}

// Sign signs the receipt with the private key and returns signed receipt.
func (r *Receipt) Sign(sk *ecdsa.PrivateKey, chainID uint64) (*SignedReceipt, error) {
	signHash, err := r.SignHash(chainID)
	if err != nil {
		return nil, err
	}

	sig, err := crypto.Sign(accounts.TextHash(signHash[:]), sk)
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
func (sr *SignedReceipt) RecoverPubKey(chainID uint64) (*ecdsa.PublicKey, error) {
	signHash, err := sr.Receipt.SignHash(chainID)
	if err != nil {
		return nil, err
	}

	return crypto.SigToPub(accounts.TextHash(signHash[:]), sr.Signature)
}
