package policy

import (
	"crypto/ecdsa"

	"github.com/ethereum/go-ethereum/common"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/storage"
)

type Storage struct {
	storage.Cyclic[int64, *Policy]
}

type Policy struct {
	cpolicy.SigningPolicy

	PublicKeys map[common.Address]*ecdsa.PublicKey
}
