package voting

import (
	"crypto/ecdsa"
	"encoding/binary"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type Receipt struct {
	InstructionHash               common.Hash `json:"instructionHash"`
	Sequence                      uint64      `json:"sequence"`
	AdditionalVariableMessageHash common.Hash `json:"additionalVariableMessageHash"`
	Timestamp                     uint64      `json:"timestamp"`
	VoteHash                      common.Hash `json:"voteHash"`
}

type SignedReceipt struct {
	Receipt   Receipt       `json:"receipt"`
	Signature hexutil.Bytes `json:"signature"`
}

func (r *Receipt) Hash() common.Hash {
	return crypto.Keccak256Hash(
		r.InstructionHash[:],
		binary.BigEndian.AppendUint64(nil, r.Sequence),
		r.AdditionalVariableMessageHash[:],
		binary.BigEndian.AppendUint64(nil, r.Timestamp),
		r.VoteHash[:],
	)
}

func (r *Receipt) Sign(pk *ecdsa.PrivateKey) (*SignedReceipt, error) {
	h := r.Hash()

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
