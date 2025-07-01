package instruction

import (
	"context"
	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
)

type Service struct {
	*voting.Manager

	pk *ecdsa.PrivateKey
}

func (s *Service) ServeInstruction(_ context.Context, i *instruction.Instruction) (*SignedReceipt, error) {
	r, err := s.Process(i)
	if err != nil {
		return nil, err
	}

	return s.SignReceipt(r)
}

func (s *Service) SignReceipt(r *voting.Receipt) (*SignedReceipt, error) {
	h := r.Hash()

	sig, err := crypto.Sign(accounts.TextHash(h[:]), s.pk)
	if err != nil {
		return nil, err
	}

	sr := &SignedReceipt{
		Receipt:   *r,
		Signature: sig,
	}

	return sr, nil
}

type SignedReceipt struct {
	Receipt   voting.Receipt
	Signature hexutil.Bytes
}
