// Package chain holds the contract ABI items both chain sources read.
package chain

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
)

var (
	// SigningPolicyInitialized is the Relay event that publishes a reward epoch's signing policy.
	SigningPolicyInitialized = MustEvent(MustABI(relay.RelayMetaData), "SigningPolicyInitialized")
	// VoterRegistered is the VoterRegistry event that records a voter's signing-policy key.
	VoterRegistered = MustEvent(MustABI(registry.RegistryMetaData), "VoterRegistered")
	// SignNewSigningPolicy is the FlareSystemsManager method by which voters sign the next signing policy.
	SignNewSigningPolicy = MustMethod(MustABI(system.FlareSystemsManagerMetaData), "signNewSigningPolicy")
)

// MustABI returns the parsed ABI of a binding's metadata, panicking if it does not parse.
func MustABI(md *bind.MetaData) *abi.ABI {
	a, err := md.GetAbi()
	if err != nil {
		panic(fmt.Errorf("loading ABI: %w", err))
	}
	return a
}

// MustEvent returns a's event name, panicking if a has none.
func MustEvent(a *abi.ABI, name string) abi.Event {
	e, ok := a.Events[name]
	if !ok {
		panic(errors.New(name + " event not found in ABI"))
	}
	return e
}

// MustMethod returns a's method name, panicking if a has none.
func MustMethod(a *abi.ABI, name string) abi.Method {
	m, ok := a.Methods[name]
	if !ok {
		panic(errors.New(name + " method not found in ABI"))
	}
	return m
}

// MustError returns a's custom error name, panicking if a has none.
func MustError(a *abi.ABI, name string) abi.Error {
	e, ok := a.Errors[name]
	if !ok {
		panic(errors.New(name + " error not found in ABI"))
	}
	return e
}
