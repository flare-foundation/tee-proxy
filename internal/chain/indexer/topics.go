package indexer

import (
	"github.com/ethereum/go-ethereum/common"
)

// addressToHash zero prefixes address to 32 bytes, as an indexed address topic.
func addressToHash(a common.Address) common.Hash {
	h := common.Hash{}
	copy(h[12:], a[:])
	return h
}
