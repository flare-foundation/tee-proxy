package jsonrpc

import (
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
)

var (
	signingPolicySignedEventSel = contracts.MustEvent(contracts.MustABI(system.FlareSystemsManagerMetaData), "SigningPolicySigned").ID

	// noActiveMachinePathListSel is the revert selector of getActiveMachinePathListNonce
	// for an extension without a signed list.
	noActiveMachinePathListSel = contracts.MustError(contracts.MustABI(machinepathmanager.MachinePathManagerMetaData), "NoActiveMachinePathList").ID.Bytes()[:4]

	// initialRewardEpochIdSel calls Relay.initialRewardEpochId(), which the binding lacks.
	initialRewardEpochIdSel = crypto.Keccak256([]byte("initialRewardEpochId()"))[:4]
)
