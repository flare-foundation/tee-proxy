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
