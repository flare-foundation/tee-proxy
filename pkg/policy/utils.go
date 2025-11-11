package policy

import (
	"github.com/ethereum/go-ethereum/common"
)

// AddressToHash zero prefixes address to 32 bytes.
func AddressToHash(a common.Address) common.Hash {
	h := common.Hash{}
	copy(h[12:], a[:])
	return h
}
