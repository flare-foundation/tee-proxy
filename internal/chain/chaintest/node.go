// Package chaintest provides an in-memory C-chain for testing the chain sources: a fake
// node serving the JSON-RPC methods the proxy uses, and a builder that records the same
// history in an indexer database.
package chaintest

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
)

// Handler answers a contract call with the method's outputs, or an error; return a
// RevertError to make the call revert.
type Handler func(args []any) ([]any, error)

// RevertError is a contract revert carrying data, typically a custom error selector.
// Empty, it is sent as nodes send a revert without data: code -32000, no data.
type RevertError []byte

func (r RevertError) Error() string { return "execution reverted" }

func (r RevertError) ErrorCode() int {
	if len(r) == 0 {
		return -32000
	}
	return 3
}

func (r RevertError) ErrorData() any {
	if len(r) == 0 {
		return nil
	}
	return hexutil.Encode(r)
}

// transient carries data, as some providers' rate-limit errors do.
type transient struct{}

func (transient) Error() string  { return "limit exceeded" }
func (transient) ErrorCode() int { return -32005 }
func (transient) ErrorData() any { return map[string]any{"backoff_seconds": 1} }

type tx struct {
	to    common.Address
	input []byte
}

type call struct {
	method abi.Method
	handle Handler
}

// Node is a fake Flare C-chain node.
type Node struct {
	// MaxLogsRange caps eth_getLogs ranges like public nodes do; 0 is unlimited.
	MaxLogsRange uint64

	mu        sync.Mutex
	chainID   uint64
	head      uint64
	headTime  uint64
	headHash  common.Hash
	logs      []types.Log
	txs       map[common.Hash][]tx // by block hash, in index order
	calls     map[common.Address]map[string]call
	failNext  int
	requests  map[string]int
	server    *httptest.Server
	rpcServer *rpc.Server
}

// NewNode starts a fake node for chainID, stopped when t ends.
func NewNode(t *testing.T, chainID uint64) *Node {
	t.Helper()

	n := &Node{
		chainID:  chainID,
		txs:      make(map[common.Hash][]tx),
		calls:    make(map[common.Address]map[string]call),
		requests: make(map[string]int),
	}

	n.rpcServer = rpc.NewServer()
	if err := n.rpcServer.RegisterName("eth", &ethService{n: n}); err != nil {
		t.Fatalf("registering fake eth service: %v", err)
	}
	n.server = httptest.NewServer(n.rpcServer)
	t.Cleanup(func() {
		n.server.Close()
		n.rpcServer.Stop()
	})

	return n
}

// URL is the node's JSON-RPC endpoint.
func (n *Node) URL() string {
	return n.server.URL
}

// BlockHash is the hash of block number on this chain.
func BlockHash(number uint64) common.Hash {
	return crypto.Keccak256Hash(fmt.Appendf(nil, "block-%d", number))
}

// SetHead sets the newest block; its hash is BlockHash(number) unless hash is given.
func (n *Node) SetHead(number, timestamp uint64, hash ...common.Hash) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.head, n.headTime, n.headHash = number, timestamp, BlockHash(number)
	if len(hash) > 0 {
		n.headHash = hash[0]
	}
}

// AddLog records l; its block hash is set from the block number.
func (n *Node) AddLog(l types.Log) {
	n.mu.Lock()
	defer n.mu.Unlock()

	l.BlockHash = BlockHash(l.BlockNumber)
	n.logs = append(n.logs, l)
}

// AddTx records a transaction to to with input in block and returns its index there.
func (n *Node) AddTx(block uint64, to common.Address, input []byte) uint {
	n.mu.Lock()
	defer n.mu.Unlock()

	h := BlockHash(block)
	n.txs[h] = append(n.txs[h], tx{to: to, input: input})

	return uint(len(n.txs[h]) - 1)
}

// Handle answers calls of method on the contract at address, described by contract.
func (n *Node) Handle(address common.Address, contract *abi.ABI, method string, h Handler) {
	n.mu.Lock()
	defer n.mu.Unlock()

	m, ok := contract.Methods[method]
	if !ok {
		panic("unknown method " + method)
	}
	if n.calls[address] == nil {
		n.calls[address] = make(map[string]call)
	}
	n.calls[address][string(m.ID)] = call{method: m, handle: h}
}

// HandleRaw answers calls with selector on address with fixed output, for methods
// no binding describes.
func (n *Node) HandleRaw(address common.Address, selector []byte, output []byte, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.calls[address] == nil {
		n.calls[address] = make(map[string]call)
	}
	n.calls[address][string(selector)] = call{handle: func([]any) ([]any, error) {
		if err != nil {
			return nil, err
		}
		return []any{output}, nil
	}}
}

// SetBatchLimit makes the node reject batches of more than items calls, as some providers do.
func (n *Node) SetBatchLimit(items int) {
	n.rpcServer.SetBatchLimits(items, 0)
}

// FailNext makes the next k requests fail with a transient error.
func (n *Node) FailNext(k int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failNext = k
}

// Requests returns how many requests of method (e.g. "eth_getLogs") were served or failed.
func (n *Node) Requests(method string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.requests[method]
}

// begin counts a request and reports whether it should fail transiently.
func (n *Node) begin(method string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.requests[method]++
	if n.failNext > 0 {
		n.failNext--
		return transient{}
	}
	return nil
}

type ethService struct {
	n *Node
}

func (s *ethService) ChainId(context.Context) (*hexutil.Big, error) { //nolint:revive // eth_chainId
	if err := s.n.begin("eth_chainId"); err != nil {
		return nil, err
	}
	return (*hexutil.Big)(new(big.Int).SetUint64(s.n.chainID)), nil
}

func (s *ethService) GetBlockByNumber(_ context.Context, tag string, _ bool) (map[string]any, error) {
	if err := s.n.begin("eth_getBlockByNumber"); err != nil {
		return nil, err
	}
	if tag != "latest" {
		return nil, errors.New("only latest is served")
	}

	s.n.mu.Lock()
	defer s.n.mu.Unlock()

	// C-chain headers carry fields go-ethereum's header type does not know
	return map[string]any{
		"hash":         s.n.headHash,
		"number":       hexutil.Uint64(s.n.head),
		"timestamp":    hexutil.Uint64(s.n.headTime),
		"extDataHash":  common.Hash{1},
		"blockGasCost": "0x0",
	}, nil
}

type callArgs struct {
	To    *common.Address `json:"to"`
	Input hexutil.Bytes   `json:"input"`
	Data  hexutil.Bytes   `json:"data"`
}

func (s *ethService) Call(_ context.Context, args callArgs, _ *string) (hexutil.Bytes, error) {
	if err := s.n.begin("eth_call"); err != nil {
		return nil, err
	}

	input := args.Input
	if len(input) == 0 {
		input = args.Data
	}
	if args.To == nil || len(input) < 4 {
		return nil, errors.New("invalid call")
	}

	s.n.mu.Lock()
	c, ok := s.n.calls[*args.To][string(input[:4])]
	s.n.mu.Unlock()
	if !ok {
		return nil, RevertError(nil)
	}

	if c.method.ID == nil {
		out, err := c.handle(nil)
		if err != nil {
			return nil, err
		}
		raw, ok := out[0].([]byte)
		if !ok {
			return nil, errors.New("raw handler must return bytes")
		}
		return raw, nil
	}

	in, err := c.method.Inputs.Unpack(input[4:])
	if err != nil {
		return nil, fmt.Errorf("unpacking %s inputs: %w", c.method.Name, err)
	}
	out, err := c.handle(in)
	if err != nil {
		return nil, err
	}
	packed, err := c.method.Outputs.Pack(out...)
	if err != nil {
		return nil, fmt.Errorf("packing %s outputs: %w", c.method.Name, err)
	}
	return packed, nil
}

type filterArgs struct {
	Address   []common.Address `json:"address"`
	Topics    [][]common.Hash  `json:"topics"`
	FromBlock string           `json:"fromBlock"`
	ToBlock   string           `json:"toBlock"`
}

func (s *ethService) GetLogs(_ context.Context, f filterArgs) ([]types.Log, error) {
	if err := s.n.begin("eth_getLogs"); err != nil {
		return nil, err
	}

	s.n.mu.Lock()
	defer s.n.mu.Unlock()

	from, err := s.n.blockArg(f.FromBlock)
	if err != nil {
		return nil, err
	}
	to, err := s.n.blockArg(f.ToBlock)
	if err != nil {
		return nil, err
	}
	if s.n.MaxLogsRange > 0 && to >= from && to-from+1 > s.n.MaxLogsRange {
		return nil, fmt.Errorf("requested too many blocks from %d to %d, maximum is set to %d", from, to, s.n.MaxLogsRange)
	}

	out := []types.Log{}
	for _, l := range s.n.logs {
		if l.BlockNumber < from || l.BlockNumber > to || !matches(l, f) {
			continue
		}
		out = append(out, l)
	}

	return out, nil
}

func (n *Node) blockArg(arg string) (uint64, error) {
	if arg == "" || arg == "latest" {
		return n.head, nil
	}
	return hexutil.DecodeUint64(arg)
}

func matches(l types.Log, f filterArgs) bool {
	if len(f.Address) > 0 {
		found := false
		for _, a := range f.Address {
			found = found || a == l.Address
		}
		if !found {
			return false
		}
	}

	for i, set := range f.Topics {
		if len(set) == 0 {
			continue
		}
		if i >= len(l.Topics) {
			return false
		}
		found := false
		for _, h := range set {
			found = found || h == l.Topics[i]
		}
		if !found {
			return false
		}
	}

	return true
}

func (s *ethService) GetTransactionByBlockHashAndIndex(_ context.Context, block common.Hash, index hexutil.Uint64) (map[string]any, error) {
	if err := s.n.begin("eth_getTransactionByBlockHashAndIndex"); err != nil {
		return nil, err
	}

	s.n.mu.Lock()
	defer s.n.mu.Unlock()

	txs := s.n.txs[block]
	if int(index) >= len(txs) {
		return nil, nil
	}
	t := txs[index]

	return map[string]any{
		"to":    t.to,
		"input": hexutil.Bytes(t.input),
	}, nil
}
