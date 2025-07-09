package policy

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// TODO: FIND PLACE FOR THIS

func SafeUint32(b *big.Int) (uint32, error) {
	idNegative := b.Sign() == -1
	idOverflow := b.BitLen() > 32

	if idNegative || idOverflow {
		return 0, errors.New("not uint32")
	}

	u := uint32(b.Uint64()) //nolint:gosec // if above checks for under and overflow

	return u, nil
}

func SafeUint64(b *big.Int) (uint64, error) {
	if b.IsUint64() {
		return b.Uint64(), nil
	}

	return 0, errors.New("not uint64")
}

func AddressToQueryParam(a common.Address) string {
	return hex.EncodeToString(a[:])
}

func AddressToHash(a common.Address) common.Hash {
	h := common.Hash{}
	copy(h[12:], a[:])
	return h
}

func Uint32ToHash(n uint32) common.Hash {
	h := common.Hash{}
	binary.BigEndian.PutUint32(h[28:], n)
	return h
}
