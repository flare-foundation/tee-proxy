// Package chain holds the contract ABI items both chain sources read.
package chain

import (
	"github.com/flare-foundation/go-flare-common/pkg/contracts"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
)

var (
	// SigningPolicyInitialized is the Relay event that publishes a reward epoch's signing policy.
	SigningPolicyInitialized = contracts.MustEvent(contracts.MustABI(relay.RelayMetaData), "SigningPolicyInitialized")
	// VoterRegistered is the VoterRegistry event that records a voter's signing-policy key.
	VoterRegistered = contracts.MustEvent(contracts.MustABI(registry.RegistryMetaData), "VoterRegistered")
	// SignNewSigningPolicy is the FlareSystemsManager method by which voters sign the next signing policy.
	SignNewSigningPolicy = contracts.MustMethod(contracts.MustABI(system.FlareSystemsManagerMetaData), "signNewSigningPolicy")
)
