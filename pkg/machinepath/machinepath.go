// Package machinepath assembles SET_MACHINE_PATH_LIST direct actions for the
// TEE node from governance-signed machine path lists recorded on chain by the
// MachinePathManager facet.
package machinepath

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-proxy/internal/queue"
)

var errNoPaths = errors.New("no machine paths found for list")

// ErrNoAuthorization marks an activated list with no forwardable authorization
// evidence: neither direct governance signatures nor a verifiable Safe approval.
// A governance-configuration condition, not an infra fault — callers classify it
// apart from other build failures.
var ErrNoAuthorization = errors.New("no governance signatures or safe approvals found for list")

// SetMachinePathListAction builds the SET_MACHINE_PATH_LIST direct action for
// the governance-approved list identified by (extensionID, nonce). It reads the
// list's paths in on-chain order and its governance authorization from src: the
// governance signatures and — when the node's governance is Safe-backed
// (gov.Safe nonzero) — one locally pre-verified Safe approval. At least one form
// must be found; otherwise ErrNoAuthorization is returned. The TEE node
// re-verifies either against its own governance set and threshold; gov is
// that same snapshot, as reported by the node.
func SetMachinePathListAction(
	ctx context.Context,
	src Source,
	managerAddress common.Address,
	gov types.Governance,
	extensionID common.Hash,
	chainID uint64,
	nonce uint64,
) (*types.Action, error) {
	list, err := src.SignedList(ctx, extensionID, nonce, gov)
	if err != nil {
		return nil, fmt.Errorf("fetching machine path list: %w", err)
	}
	if len(list.Paths) == 0 {
		return nil, errNoPaths
	}

	dataHash, err := types.MachinePathListDataHash(extensionID, nonce, list.Paths)
	if err != nil {
		return nil, fmt.Errorf("computing machine path list data hash: %w", err)
	}
	hash, err := csigning.NewPayload(csigning.TEEMachinePathList, chainID, dataHash).Hash()
	if err != nil {
		return nil, fmt.Errorf("computing machine path list message hash: %w", err)
	}

	sigs := collectSignatures(list.Signatures, hash)

	var approval *safe.Approval
	if gov.Safe != (common.Address{}) {
		approval, err = selectApproval(list.Approvals, gov, managerAddress, extensionID, nonce, hash, chainID)
		if err != nil {
			return nil, fmt.Errorf("selecting safe approval: %w", err)
		}
	}

	if len(sigs) == 0 && approval == nil {
		return nil, ErrNoAuthorization
	}

	req := types.SetMachinePathListRequest{
		Paths:        list.Paths,
		Nonce:        nonce,
		Signatures:   sigs,
		SafeApproval: approval,
	}

	msg, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling set machine path list request: %w", err)
	}

	return queue.PrepareDirectAction(op.Governance, op.SetMachinePathList, msg)
}

// collectSignatures serializes the list's governance signatures, keeping the
// first per signer recovered against hash. An empty result is not an error —
// the caller decides whether other authorization evidence (a Safe approval)
// covers the list.
func collectSignatures(signatures []machinepathmanager.Signature, hash common.Hash) [][]byte {
	sigs := make([][]byte, 0, len(signatures))
	seen := make(map[common.Address]bool, len(signatures))

	for _, sig := range signatures {
		serialized := serializeSig(sig)
		signer, err := teeutils.SignatureToSignersAddress(hash[:], serialized)
		if err != nil || seen[signer] {
			continue
		}

		seen[signer] = true
		sigs = append(sigs, serialized)
	}

	return sigs
}

// serializeSig serializes a contract Signature to [R||S||V-27].
func serializeSig(s machinepathmanager.Signature) []byte {
	sig := make([]byte, 0, 65)
	sig = append(sig, s.R[:]...)
	sig = append(sig, s.S[:]...)
	sig = append(sig, s.V-27)

	return sig
}
