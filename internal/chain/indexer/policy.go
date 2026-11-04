package indexer

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"

	"github.com/flare-foundation/tee-proxy/internal/chain"
)

// LatestSigningPolicies returns up to n most recent SigningPolicyInitialized events, newest first.
func (s *Source) LatestSigningPolicies(ctx context.Context, n int) ([]*relay.RelaySigningPolicyInitialized, error) {
	logs, err := database.FetchLatestLogsByAddressAndTopic0(ctx, s.db, database.LatestLogsParams{
		Address: s.addresses.Relay,
		Topic0:  chain.SigningPolicyInitialized.ID,
		Number:  n,
	})
	if err != nil {
		return nil, err
	}

	events := make([]*relay.RelaySigningPolicyInitialized, len(logs))
	for i := range logs {
		events[i], err = cpolicy.ParseSigningPolicyInitializedEvent(logs[i])
		if err != nil {
			return nil, fmt.Errorf("parsing signing policy event: %w", err)
		}
	}

	return events, nil
}

// SigningPolicy returns the SigningPolicyInitialized event for epoch. For epoch 0
// the zero topic cannot be filtered on and the newest event is returned instead.
func (s *Source) SigningPolicy(ctx context.Context, epoch uint32) (*relay.RelaySigningPolicyInitialized, bool, error) {
	topics := [4]common.Hash{}
	topics[0] = chain.SigningPolicyInitialized.ID
	topics[1] = convert.Uint32ToHash(epoch)

	logs, err := database.FetchLogsFull(ctx, s.db, database.LogsFullParams{
		Address: s.addresses.Relay,
		Topics:  topics,
		Number:  1,
	})
	if err != nil {
		return nil, false, err
	}
	if len(logs) == 0 {
		return nil, false, nil
	}

	event, err := cpolicy.ParseSigningPolicyInitializedEvent(logs[0])
	if err != nil {
		return nil, false, fmt.Errorf("parsing signing policy %d event: %w", epoch, err)
	}

	return event, true, nil
}

// VoterRegistrations returns, per signing-policy address, the VoterRegistered events
// emitted by registryAddress for epoch.
func (s *Source) VoterRegistrations(ctx context.Context, registryAddress common.Address, epoch uint32, signers []common.Address) (map[common.Address][]*registry.RegistryVoterRegistered, error) {
	out := make(map[common.Address][]*registry.RegistryVoterRegistered, len(signers))

	for _, signer := range signers {
		topics := [4]common.Hash{}
		topics[0] = chain.VoterRegistered.ID
		topics[2] = convert.Uint32ToHash(epoch)
		topics[3] = addressToHash(signer)

		logs, err := database.FetchLogsFull(ctx, s.db, database.LogsFullParams{
			Address: registryAddress,
			Topics:  topics,
			Number:  -1,
		})
		if err != nil {
			return nil, fmt.Errorf("fetching voter registered events for %v: %w", signer, err)
		}

		events := make([]*registry.RegistryVoterRegistered, len(logs))
		for i := range logs {
			events[i], err = cpolicy.ParseVoterRegisteredEvent(logs[i])
			if err != nil {
				return nil, fmt.Errorf("parsing voter registered event: %w", err)
			}
		}
		out[signer] = events
	}

	return out, nil
}

// PolicySignatures returns the calldata of successful signNewSigningPolicy transactions
// sent to the FlareSystemsManager in blocks (from, to]. Calls made through another
// contract are not indexed as such and are missed.
func (s *Source) PolicySignatures(ctx context.Context, _ uint32, from, to uint64) ([][]byte, error) {
	txs, err := database.FetchTransactionsByAddressAndSelectorBlockNumber(ctx, s.db, database.TxParams{
		ToAddress:   s.addresses.FlareSystemsManager,
		FunctionSel: [4]byte(chain.SignNewSigningPolicy.ID),
		From:        int64(from),
		To:          int64(to),
	})
	if err != nil {
		return nil, err
	}

	inputs := make([][]byte, 0, len(txs))
	for i := range txs {
		input, err := hex.DecodeString(txs[i].Input)
		if err != nil {
			continue
		}
		inputs = append(inputs, input)
	}

	return inputs, nil
}
