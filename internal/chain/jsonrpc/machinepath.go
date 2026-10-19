package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
)

var errListNotSigned = errors.New("machine path list is not signed")

// LatestSignedNonce returns the extension's active list nonce — the highest signed —
// when it is above after.
func (s *Source) LatestSignedNonce(ctx context.Context, extensionID common.Hash, after uint64) (uint64, bool, error) {
	var nonce *big.Int
	err := s.retry(ctx, func(ctx context.Context) (err error) {
		nonce, err = s.manager.GetActiveMachinePathListNonce(s.opts(ctx), extensionID.Big())
		return err
	})
	if revertedWith(err, noActiveMachinePathListSel) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("fetching active machine path list nonce: %w", err)
	}
	if !nonce.IsUint64() || nonce.Uint64() <= after {
		return 0, false, nil
	}

	return nonce.Uint64(), true, nil
}

type list struct {
	Paths                    []machinepathmanager.IMachinePathManagerMachinePath
	InvolvedGovernanceHashes [][32]byte
	Signatures               []machinepathmanager.Signature
	Signed                   bool
}

// SignedList returns the activated list (extensionID, nonce) as the contract stores it:
// paths in on-chain order, every governance signature submitted (including after
// activation) and, under Safe-backed governance, the confirmed Safe approval for gov.Hash.
func (s *Source) SignedList(ctx context.Context, extensionID common.Hash, nonce uint64, gov types.Governance) (machinepath.List, error) {
	ext, n := extensionID.Big(), new(big.Int).SetUint64(nonce)

	var l list
	err := s.retry(ctx, func(ctx context.Context) error {
		out, err := s.manager.GetMachinePathList(s.opts(ctx), ext, n)
		l = list(out)
		return err
	})
	if err != nil {
		return machinepath.List{}, fmt.Errorf("fetching machine path list: %w", err)
	}
	if !l.Signed {
		return machinepath.List{}, errListNotSigned
	}

	paths := make([]cmpaths.IMachinePathManagerMachinePath, len(l.Paths))
	for i, p := range l.Paths {
		paths[i] = cmpaths.IMachinePathManagerMachinePath{SourceTeeIds: p.SourceTeeIds, DestinationTeeIds: p.DestinationTeeIds}
	}
	out := machinepath.List{Paths: paths, Signatures: l.Signatures}

	if gov.Safe == (common.Address{}) {
		return out, nil
	}
	if !slices.Contains(l.InvolvedGovernanceHashes, [32]byte(gov.Hash)) {
		logger.Debugf("machine path list %d does not involve governance %s", nonce, gov.Hash)
		return out, nil
	}

	approval, ok, err := s.safeApproval(ctx, ext, n, gov.Hash)
	if err != nil {
		return machinepath.List{}, err
	}
	if ok {
		out.Approvals = []safe.Approval{approval}
	}

	return out, nil
}

// safeApproval rebuilds the Safe approval confirmed on chain for governanceHash.
// confirmMachinePathListSafeApproval verifies the owners' signatures against a fixed
// transaction — a plain CALL of approveMachinePathList on the manager with zero value
// and gas fields — so that transaction is the one the signatures cover.
func (s *Source) safeApproval(ctx context.Context, ext, nonce *big.Int, governanceHash common.Hash) (safe.Approval, bool, error) {
	var artifact machinepathmanager.IMachinePathManagerSafeApprovalArtifact
	err := s.retry(ctx, func(ctx context.Context) (err error) {
		artifact, err = s.manager.GetMachinePathListSafeApprovalArtifact(s.opts(ctx), ext, nonce, governanceHash)
		return err
	})
	if err != nil {
		return safe.Approval{}, false, fmt.Errorf("fetching safe approval artifact: %w", err)
	}
	if len(artifact.Signatures) == 0 || artifact.SafeNonce == nil || !artifact.SafeNonce.IsUint64() {
		return safe.Approval{}, false, nil
	}

	var messageHash [32]byte
	err = s.retry(ctx, func(ctx context.Context) (err error) {
		messageHash, err = s.manager.GetMachinePathListMessageHash(s.opts(ctx), ext, nonce)
		return err
	})
	if err != nil {
		return safe.Approval{}, false, fmt.Errorf("fetching machine path list message hash: %w", err)
	}

	calldata, err := types.ApproveMachinePathListCalldata(common.BigToHash(ext), nonce.Uint64(), messageHash)
	if err != nil {
		return safe.Approval{}, false, fmt.Errorf("building approveMachinePathList calldata: %w", err)
	}

	return safe.Approval{
		ExecTransaction: safe.ExecTransactionInputs{
			To:         s.addresses.MachinePathManager,
			Value:      new(big.Int),
			Data:       calldata,
			Operation:  safe.OperationCall,
			SafeTxGas:  new(big.Int),
			BaseGas:    new(big.Int),
			GasPrice:   new(big.Int),
			Signatures: artifact.Signatures,
		},
		Nonce: artifact.SafeNonce.Uint64(),
	}, true, nil
}
