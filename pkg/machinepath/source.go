package machinepath

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
)

// Source supplies the on-chain record of machine path lists.
// Implementations only fetch; every check on the data is done in this package.
type Source interface {
	// LatestSignedNonce returns the highest nonce above after among the extension's
	// activated (signed) lists; found is false when there is none.
	LatestSignedNonce(ctx context.Context, extensionID common.Hash, after uint64) (nonce uint64, found bool, err error)

	// SignedList returns the activated list (extensionID, nonce) as recorded on chain.
	// Safe approval candidates are fetched only when gov is Safe-backed.
	SignedList(ctx context.Context, extensionID common.Hash, nonce uint64, gov types.Governance) (List, error)
}

// List is an activated machine path list as recorded on chain; nothing in it is verified.
type List struct {
	// Paths in on-chain order, which the list's message hash commits to.
	Paths []cmpaths.IMachinePathManagerMachinePath
	// Signatures are the governance signatures the contract accepted for the list.
	Signatures []machinepathmanager.Signature
	// Approvals are Safe approval candidates for the list.
	Approvals []safe.Approval
}
