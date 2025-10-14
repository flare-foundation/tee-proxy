package policy

import (
	"encoding/binary"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// SafeUint32 converts bit.Int into uint32.
// It returns an error if it is out of bounds.
func SafeUint32(b *big.Int) (uint32, error) {
	idNegative := b.Sign() == -1
	idOverflow := b.BitLen() > 32

	if idNegative || idOverflow {
		return 0, errors.New("not uint32")
	}

	u := uint32(b.Uint64()) //nolint:gosec // if statement above checks for under and overflow

	return u, nil
}

// SafeUint64 converts bit.Int into uint64.
// It returns an error if it is out of bounds.
func SafeUint64(b *big.Int) (uint64, error) {
	if b.IsUint64() {
		return b.Uint64(), nil
	}

	return 0, errors.New("not uint64")
}

// AddressToHash zero prefixes address to 32 bytes.
func AddressToHash(a common.Address) common.Hash {
	h := common.Hash{}
	copy(h[12:], a[:])
	return h
}

// Uint32ToHash hex encodes uint32 and zero prefixes to 32 bytes.
func Uint32ToHash(n uint32) common.Hash {
	h := common.Hash{}
	binary.BigEndian.PutUint32(h[28:], n)
	return h
}
