// Package jsonrpc reads the proxy's on-chain data from a Flare C-chain node's JSON-RPC endpoint.
package jsonrpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"

	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/machinepath"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

// retryBudget bounds the retries of one read, or one attempt when the timeout is longer.
// It is a var so tests can shrink it.
var retryBudget = 15 * time.Second

const (
	retryMinDelay = 200 * time.Millisecond
	retryMaxDelay = 3 * time.Second

	// revertCode is the JSON-RPC error code of a revert carrying data.
	revertCode = 3
)

var (
	errChainIDMismatch = errors.New("node chain ID does not match chain_id")
	errRelaySetter     = errors.New("relay's signing policy setter is not the configured flare_systems_manager")
	errNoBlock         = errors.New("node returned no latest block")
)

// Source answers on-chain queries from a node's JSON-RPC endpoint.
type Source struct {
	rpc       *rpc.Client
	eth       *ethclient.Client
	secrets   []string // kept out of errors
	timeout   time.Duration
	logsRange uint64
	batchSize int
	sync      database.SyncParams

	addresses config.Addresses
	fsm       *system.FlareSystemsManagerCaller
	relay     *relay.RelayCaller
	manager   *machinepathmanager.MachinePathManagerCaller

	// initialEpoch is the relay's first epoch, seeded without a SigningPolicyInitialized event.
	initialEpoch uint32
}

var (
	_ policy.Source      = (*Source)(nil)
	_ machinepath.Source = (*Source)(nil)
)

// Dial connects to the endpoint in cfg and checks that it serves chainID and that the
// relay at addresses is fed by the configured FlareSystemsManager; sync bounds WaitSynced.
func Dial(ctx context.Context, cfg config.Chain, addresses config.Addresses, chainID uint64, sync database.SyncParams) (*Source, error) {
	endpoint, opts, err := endpointURL(cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing chain url: %w", err)
	}
	secrets := []string{endpoint, cfg.URL, url.QueryEscape(cfg.APIKey), cfg.APIKey}

	client, err := rpc.DialOptions(ctx, endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dialing chain node: %w", redact(err, secrets...))
	}

	s := &Source{
		rpc:       client,
		eth:       ethclient.NewClient(client),
		secrets:   secrets,
		timeout:   cfg.Timeout,
		logsRange: cfg.MaxLogsRange,
		batchSize: cfg.MaxBatchSize,
		sync:      sync,
		addresses: addresses,
	}

	if err := s.bind(); err != nil {
		client.Close()
		return nil, err
	}
	if err := s.check(ctx, chainID); err != nil {
		client.Close()
		return nil, err
	}

	return s, nil
}

// endpointURL is cfg.URL with cfg.APIKey as the x-apikey query parameter and its user
// info as a basic auth header, in the form transport errors print so redaction matches.
func endpointURL(cfg config.Chain) (string, []rpc.ClientOption, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		// the url.Error quotes the URL, escaping what redaction would match
		return "", nil, errors.Unwrap(err)
	}

	var opts []rpc.ClientOption
	if u.User != nil {
		password, _ := u.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + password))
		opts = append(opts, rpc.WithHeader("Authorization", "Basic "+auth))
		u.User = nil
	}
	if cfg.APIKey != "" {
		q := u.Query()
		q.Set("x-apikey", cfg.APIKey)
		u.RawQuery = q.Encode()
	}

	return u.String(), opts, nil
}

func (s *Source) bind() error {
	var err error
	if s.fsm, err = system.NewFlareSystemsManagerCaller(s.addresses.FlareSystemsManager, s.eth); err != nil {
		return fmt.Errorf("binding flare systems manager: %w", err)
	}
	if s.relay, err = relay.NewRelayCaller(s.addresses.Relay, s.eth); err != nil {
		return fmt.Errorf("binding relay: %w", err)
	}
	if s.manager, err = machinepathmanager.NewMachinePathManagerCaller(s.addresses.MachinePathManager, s.eth); err != nil {
		return fmt.Errorf("binding machine path manager: %w", err)
	}
	return nil
}

// check verifies the node and contracts before any data is read.
func (s *Source) check(ctx context.Context, chainID uint64) error {
	var id *big.Int
	err := s.retry(ctx, func(ctx context.Context) (err error) {
		id, err = s.eth.ChainID(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("fetching chain ID: %w", err)
	}
	if !id.IsUint64() || id.Uint64() != chainID {
		return fmt.Errorf("%w: node %v, configured %d", errChainIDMismatch, id, chainID)
	}

	var setter common.Address
	err = s.retry(ctx, func(ctx context.Context) (err error) {
		setter, err = s.relay.SigningPolicySetter(s.opts(ctx))
		return err
	})
	if err != nil {
		return fmt.Errorf("fetching relay's signing policy setter: %w", err)
	}
	// FSM block pointers locate the relay's policy events only if the FSM sets them
	if setter != s.addresses.FlareSystemsManager {
		return fmt.Errorf("%w: setter %s", errRelaySetter, setter)
	}

	var out hexutil.Bytes
	err = s.retry(ctx, func(ctx context.Context) (err error) {
		out, err = s.eth.CallContract(ctx, ethereum.CallMsg{To: &s.addresses.Relay, Data: initialRewardEpochIdSel}, nil)
		return err
	})
	switch {
	case isRevert(err) || (err == nil && len(out) != 32):
		logger.Warnf("relay %s has no initialRewardEpochId; assuming its history starts at epoch 1", s.addresses.Relay)
	case err != nil:
		return fmt.Errorf("fetching relay's initial reward epoch: %w", err)
	default:
		if s.initialEpoch, err = convert.BigToUint32Safe(new(big.Int).SetBytes(out)); err != nil {
			return fmt.Errorf("relay's initial reward epoch: %w", err)
		}
	}

	return nil
}

// Close releases the connection to the node.
func (s *Source) Close() {
	s.rpc.Close()
}

type block struct {
	Hash      common.Hash    `json:"hash"`
	Number    hexutil.Uint64 `json:"number"`
	Timestamp hexutil.Uint64 `json:"timestamp"`
}

// latestBlock reads the newest block raw: C-chain headers carry fields go-ethereum's
// header type drops, so a hash recomputed from a decoded header would be wrong.
func (s *Source) latestBlock(ctx context.Context) (block, error) {
	var b *block
	err := s.retry(ctx, func(ctx context.Context) error {
		b = nil
		return s.rpc.CallContext(ctx, &b, "eth_getBlockByNumber", "latest", false)
	})
	if err != nil {
		return block{}, err
	}
	if b == nil {
		return block{}, errNoBlock
	}

	return *b, nil
}

// LatestBlockHash returns the hash of the node's newest block.
func (s *Source) LatestBlockHash(ctx context.Context) (common.Hash, error) {
	b, err := s.latestBlock(ctx)
	return b.Hash, err
}

// Head returns the number and timestamp of the node's newest block.
func (s *Source) Head(ctx context.Context) (uint64, uint64, error) {
	b, err := s.latestBlock(ctx)
	return uint64(b.Number), uint64(b.Timestamp), err
}

// WaitSynced blocks until the node's newest block is recent.
func (s *Source) WaitSynced(ctx context.Context) error {
	for k := 0; ; k++ {
		_, timestamp, err := s.Head(ctx)
		if err != nil {
			return fmt.Errorf("fetching head: %w", err)
		}

		outOfSync := time.Since(time.Unix(int64(timestamp), 0)) //nolint:gosec // block timestamps fit int64
		if outOfSync < s.sync.OutOfSyncTolerance {
			return nil
		}
		if k >= s.sync.Retries {
			return fmt.Errorf("node out of sync after %d retries: delayed for %v", s.sync.Retries, outOfSync)
		}

		sleep := max(min(s.sync.MaxSleepTime, outOfSync/20), s.sync.MinSleepTime)
		logger.Warnf("node out of sync: delayed for %v, sleeping for %v", outOfSync, sleep)
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Source) opts(ctx context.Context) *bind.CallOpts {
	return &bind.CallOpts{Context: ctx}
}

// retry runs f with a per-request timeout, retrying failures with backoff for up to
// retryBudget. Contract reverts return at once.
func (s *Source) retry(ctx context.Context, f func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, max(retryBudget, s.timeout))
	defer cancel()

	delay := retryMinDelay
	for {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, s.timeout)
		err := f(attemptCtx)
		attemptCancel()
		if err == nil || isRevert(err) {
			return err
		}

		select {
		case <-ctx.Done():
			return redact(err, s.secrets...)
		case <-time.After(delay):
		}
		delay = min(2*delay, retryMaxDelay)
	}
}

// isRevert reports whether err is a contract revert. Other errors may carry data too,
// e.g. some providers' rate limits.
func isRevert(err error) bool {
	if err == nil {
		return false
	}
	var re rpc.Error
	if errors.As(err, &re) && re.ErrorCode() == revertCode {
		return true
	}
	// nodes send a revert without data as -32000
	return strings.Contains(err.Error(), "execution reverted")
}

// revertedWith reports whether err is a revert carrying the given error selector.
func revertedWith(err error, selector []byte) bool {
	var de rpc.DataError
	if !errors.As(err, &de) {
		return false
	}
	data, ok := de.ErrorData().(string)
	if !ok {
		return false
	}
	raw, decodeErr := hexutil.Decode(data)
	return decodeErr == nil && bytes.HasPrefix(raw, selector)
}

// redactedError hides the endpoint and API key from error text.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

func redact(err error, secrets ...string) error {
	if err == nil {
		return nil
	}

	// longest first: replacing a secret inside another would break the longer one's match
	secrets = slices.SortedFunc(slices.Values(secrets), func(a, b string) int { return len(b) - len(a) })

	msg := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "<redacted>")
		}
	}
	if msg == err.Error() {
		return err
	}

	return redactedError{msg: msg, err: err}
}
