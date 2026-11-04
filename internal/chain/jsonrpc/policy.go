package jsonrpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"

	"github.com/flare-foundation/tee-proxy/internal/chain"
)

var (
	// errPolicyEventMissing is retried for the newest epoch: a load-balanced node may lag
	// the block the FSM reports.
	errPolicyEventMissing   = errors.New("no SigningPolicyInitialized event at the FSM's sign start block")
	errPolicyEventAmbiguous = errors.New("several SigningPolicyInitialized events for one epoch")
	errNoRegistrationWindow = errors.New("voter registration window not recorded by the FSM")
	// errTxMissing is retried: a load-balanced node may not have the block yet.
	errTxMissing = errors.New("node returned no transaction for a log")
)

type signInfo struct {
	startBlock, endBlock uint64
}

// signInfo returns the blocks in which epoch's signing policy was initialized and
// in which it reached the signing threshold; zero when not yet.
func (s *Source) signInfo(ctx context.Context, epoch uint32) (signInfo, error) {
	var info signInfo
	err := s.retry(ctx, func(ctx context.Context) error {
		out, err := s.fsm.GetSigningPolicySignInfo(s.opts(ctx), epochBig(epoch))
		info = signInfo{startBlock: out.SigningPolicySignStartBlock, endBlock: out.SigningPolicySignEndBlock}
		return err
	})
	if err != nil {
		return signInfo{}, fmt.Errorf("fetching signing policy %d sign info: %w", epoch, err)
	}

	return info, nil
}

// lastEpoch returns the newest epoch whose policy the relay holds.
func (s *Source) lastEpoch(ctx context.Context) (uint32, error) {
	var last uint32
	err := s.retry(ctx, func(ctx context.Context) error {
		out, err := s.relay.LastInitializedRewardEpochData(s.opts(ctx))
		last = out.LastInitializedRewardEpoch
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("fetching last initialized reward epoch: %w", err)
	}

	return last, nil
}

// LatestSigningPolicies returns up to n most recent SigningPolicyInitialized events, newest first.
func (s *Source) LatestSigningPolicies(ctx context.Context, n int) ([]*relay.RelaySigningPolicyInitialized, error) {
	last, err := s.lastEpoch(ctx)
	if err != nil {
		return nil, err
	}

	events := make([]*relay.RelaySigningPolicyInitialized, 0, n)
	for epoch := last; epoch > s.initialEpoch && len(events) < n; epoch-- {
		event, found, err := s.signingPolicy(ctx, epoch, last)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		events = append(events, event)
	}

	return events, nil
}

// SigningPolicy returns the SigningPolicyInitialized event for epoch, read from the
// block in which the FSM recorded the policy's initialization. An older epoch whose
// event the node no longer serves is not found.
func (s *Source) SigningPolicy(ctx context.Context, epoch uint32) (*relay.RelaySigningPolicyInitialized, bool, error) {
	if epoch <= s.initialEpoch {
		return nil, false, nil
	}

	last, err := s.lastEpoch(ctx)
	if err != nil {
		return nil, false, err
	}

	return s.signingPolicy(ctx, epoch, last)
}

// signingPolicy is SigningPolicy given last, the relay's newest epoch.
func (s *Source) signingPolicy(ctx context.Context, epoch, last uint32) (*relay.RelaySigningPolicyInitialized, bool, error) {
	// the FSM may initialize policies on another relay than the configured one
	if epoch > last {
		return nil, false, nil
	}

	info, err := s.signInfo(ctx, epoch)
	if err != nil {
		return nil, false, err
	}
	if info.startBlock == 0 {
		return nil, false, nil
	}

	block := new(big.Int).SetUint64(info.startBlock)
	q := ethereum.FilterQuery{
		FromBlock: block,
		ToBlock:   block,
		Addresses: []common.Address{s.addresses.Relay},
		Topics:    [][]common.Hash{{chain.SigningPolicyInitialized.ID}, {convert.Uint32ToHash(epoch)}},
	}

	var logs []types.Log
	err = s.retry(ctx, func(ctx context.Context) (err error) {
		logs, err = s.eth.FilterLogs(ctx, q)
		if err == nil && len(logs) == 0 && epoch == last {
			err = errPolicyEventMissing
		}
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("fetching signing policy %d event at block %d: %w", epoch, info.startBlock, err)
	}
	if len(logs) == 0 {
		// an older epoch's block is hours old, so the node dropped it rather than lags
		return nil, false, nil
	}
	if len(logs) > 1 {
		return nil, false, fmt.Errorf("epoch %d: %w", epoch, errPolicyEventAmbiguous)
	}

	event, err := cpolicy.RelayFilterer.ParseSigningPolicyInitialized(logs[0])
	if err != nil {
		return nil, false, fmt.Errorf("parsing signing policy %d event: %w", epoch, err)
	}

	return event, true, nil
}

// VoterRegistrations returns, per signing-policy address, the VoterRegistered events
// emitted by registryAddress for epoch, scanning the registration window the FSM
// records: from vote power block selection to the policy's initialization.
func (s *Source) VoterRegistrations(ctx context.Context, registryAddress common.Address, epoch uint32, signers []common.Address) (map[common.Address][]*registry.RegistryVoterRegistered, error) {
	var opened uint64
	err := s.retry(ctx, func(ctx context.Context) error {
		out, err := s.fsm.GetRandomAcquisitionInfo(s.opts(ctx), epochBig(epoch))
		opened = out.RandomAcquisitionEndBlock
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("fetching random acquisition info: %w", err)
	}

	info, err := s.signInfo(ctx, epoch)
	if err != nil {
		return nil, err
	}
	if opened == 0 || info.startBlock == 0 || opened > info.startBlock {
		return nil, fmt.Errorf("epoch %d: %w", epoch, errNoRegistrationWindow)
	}

	logs, err := s.filterLogs(ctx, ethereum.FilterQuery{
		Addresses: []common.Address{registryAddress},
		Topics:    [][]common.Hash{{chain.VoterRegistered.ID}, nil, {convert.Uint32ToHash(epoch)}},
	}, opened, info.startBlock)
	if err != nil {
		return nil, fmt.Errorf("fetching voter registered events: %w", err)
	}

	wanted := make(map[common.Address]bool, len(signers))
	for _, a := range signers {
		wanted[a] = true
	}

	out := make(map[common.Address][]*registry.RegistryVoterRegistered, len(signers))
	for i := range logs {
		event, err := cpolicy.RegistryFilterer.ParseVoterRegistered(logs[i])
		if err != nil {
			return nil, fmt.Errorf("parsing voter registered event: %w", err)
		}
		if wanted[event.SigningPolicyAddress] {
			out[event.SigningPolicyAddress] = append(out[event.SigningPolicyAddress], event)
		}
	}

	return out, nil
}

// PolicySignatures returns the calldata of the signNewSigningPolicy transactions sent
// to the FlareSystemsManager for epoch once the FSM records the end of signing, when
// the set is final; nothing before. from and to are not used.
func (s *Source) PolicySignatures(ctx context.Context, epoch uint32, _, _ uint64) ([][]byte, error) {
	info, err := s.signInfo(ctx, epoch)
	if err != nil {
		return nil, err
	}
	if info.startBlock == 0 || info.endBlock == 0 {
		return nil, nil
	}

	logs, err := s.filterLogs(ctx, ethereum.FilterQuery{
		Addresses: []common.Address{s.addresses.FlareSystemsManager},
		Topics:    [][]common.Hash{{signingPolicySignedEventSel}, {convert.Uint32ToHash(epoch)}},
	}, info.startBlock, info.endBlock)
	if err != nil {
		return nil, fmt.Errorf("fetching signing policy signed events: %w", err)
	}

	txs, err := s.transactions(ctx, logs)
	if err != nil {
		return nil, err
	}

	inputs := make([][]byte, 0, len(txs))
	for _, tx := range txs {
		// calls through another contract carry no decodable signature
		if tx.To == nil || *tx.To != s.addresses.FlareSystemsManager || !bytes.HasPrefix(tx.Input, chain.SignNewSigningPolicy.ID) {
			continue
		}
		inputs = append(inputs, tx.Input)
	}

	return inputs, nil
}

type transaction struct {
	To    *common.Address `json:"to"`
	Input hexutil.Bytes   `json:"input"`
}

// transactions fetches the transactions that emitted logs, located by block hash and
// index so the node's transaction-hash index is not needed.
func (s *Source) transactions(ctx context.Context, logs []types.Log) ([]*transaction, error) {
	txs := make([]*transaction, len(logs))
	for start := 0; start < len(logs); start += s.batchSize {
		end := min(start+s.batchSize, len(logs))

		batch := make([]rpc.BatchElem, 0, end-start)
		for i := start; i < end; i++ {
			batch = append(batch, rpc.BatchElem{
				Method: "eth_getTransactionByBlockHashAndIndex",
				Args:   []any{logs[i].BlockHash, hexutil.Uint64(logs[i].TxIndex)},
				Result: &txs[i],
			})
		}

		err := s.retry(ctx, func(ctx context.Context) error {
			if err := s.rpc.BatchCallContext(ctx, batch); err != nil {
				return err
			}
			for i, e := range batch {
				if e.Error != nil {
					return e.Error
				}
				// a short set would never reach the threshold
				if txs[start+i] == nil {
					return errTxMissing
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("fetching signing transactions: %w", err)
		}
	}

	return txs, nil
}

func epochBig(epoch uint32) *big.Int {
	return new(big.Int).SetUint64(uint64(epoch))
}
