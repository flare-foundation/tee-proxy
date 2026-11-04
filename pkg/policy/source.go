package policy

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
)

// Source supplies the on-chain record that signing-policy handling reads.
// Implementations only fetch; every check on the data is done in this package.
type Source interface {
	// Head returns the number and timestamp of the newest block the source holds.
	Head(ctx context.Context) (number, timestamp uint64, err error)

	// LatestSigningPolicies returns up to n most recent SigningPolicyInitialized events, newest first.
	LatestSigningPolicies(ctx context.Context, n int) ([]*relay.RelaySigningPolicyInitialized, error)

	// SigningPolicy returns the SigningPolicyInitialized event for epoch; found is false when the
	// source holds none. It may return another epoch's event, which callers reject.
	// The event's Raw.BlockNumber must be set.
	SigningPolicy(ctx context.Context, epoch uint32) (event *relay.RelaySigningPolicyInitialized, found bool, err error)

	// VoterRegistrations returns, per signing-policy address, the VoterRegistered events
	// emitted by registryAddress for epoch.
	VoterRegistrations(ctx context.Context, registryAddress common.Address, epoch uint32, signers []common.Address) (map[common.Address][]*registry.RegistryVoterRegistered, error)

	// PolicySignatures returns the calldata of successful signNewSigningPolicy transactions
	// for epoch in blocks (from, to]. It may include other epochs' calls, which callers reject.
	PolicySignatures(ctx context.Context, epoch uint32, from, to uint64) ([][]byte, error)
}
