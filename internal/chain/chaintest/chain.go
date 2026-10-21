package chaintest

import (
	"crypto/ecdsa"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/safe"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

// Addresses are the contracts of a Chain.
var Addresses = config.Addresses{
	FlareSystemsManager: common.HexToAddress("0xf5f5"),
	Relay:               common.HexToAddress("0x2e1a"),
	VoterRegistry:       common.HexToAddress("0x7e61"),
	MachinePathManager:  common.HexToAddress("0x3a7b"),
}

type listKey struct {
	extensionID common.Hash
	nonce       uint64
}

type pathList struct {
	paths      []cmpaths.IMachinePathManagerMachinePath
	involved   [][32]byte
	signatures []machinepathmanager.Signature
	signed     bool
	artifacts  map[[32]byte]machinepathmanager.IMachinePathManagerSafeApprovalArtifact
}

// Chain records one on-chain history in both an indexer database and a fake node,
// and serves the contract views the node backend reads from it.
type Chain struct {
	DB      *gorm.DB
	Node    *Node
	ChainID uint64

	mu           sync.Mutex
	initialEpoch uint32
	lastEpoch    uint32
	signStart    map[uint32]uint64
	signEnd      map[uint32]uint64
	opened       map[uint32]uint64
	lists        map[listKey]*pathList
	active       map[common.Hash]uint64
	txIndex      map[uint64]uint64
}

// New returns an empty chain with chainID whose relay history starts after initialEpoch.
func New(t *testing.T, chainID uint64, initialEpoch uint32) *Chain {
	t.Helper()

	db, _ := testutil.InMemoryDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}, &database.State{}, &database.Block{}))

	c := &Chain{
		DB:           db,
		Node:         NewNode(t, chainID),
		ChainID:      chainID,
		initialEpoch: initialEpoch,
		signStart:    make(map[uint32]uint64),
		signEnd:      make(map[uint32]uint64),
		opened:       make(map[uint32]uint64),
		lists:        make(map[listKey]*pathList),
		active:       make(map[common.Hash]uint64),
		txIndex:      make(map[uint64]uint64),
	}
	c.serveViews(t)

	return c
}

// SetHead makes number, at timestamp, the newest block in both stores.
func (c *Chain) SetHead(t *testing.T, number, timestamp uint64) {
	t.Helper()

	require.NoError(t, c.DB.Where("name = ?", "last_database_block").Delete(&database.State{}).Error)
	testutil.InsertState(t, c.DB, number, timestamp)
	require.NoError(t, c.DB.Create(&database.Block{Hash: BlockHash(number).Hex()[2:], Number: number, Timestamp: timestamp}).Error)
	c.Node.SetHead(number, timestamp)
}

// log records r in both stores.
func (c *Chain) log(t *testing.T, r testutil.LogRow, txIndex uint) {
	t.Helper()

	if r.TxHash == (common.Hash{}) {
		r.TxHash = crypto.Keccak256Hash(r.Data(t), r.Address[:], big.NewInt(int64(r.Block)).Bytes(), big.NewInt(int64(r.Index)).Bytes(), r.Event.ID[:])
	}
	testutil.InsertLog(t, c.DB, r)

	topics := append([]common.Hash{r.Event.ID}, r.Topics...)
	c.Node.AddLog(ethtypes.Log{
		Address:     r.Address,
		Topics:      topics,
		Data:        r.Data(t),
		BlockNumber: r.Block,
		TxHash:      r.TxHash,
		TxIndex:     txIndex,
		Index:       uint(r.Index),
	})
}

// tx records a transaction in both stores and returns its index in the node's
// block and its hash. Reverted transactions emit no event, so only the indexer has them.
func (c *Chain) tx(t *testing.T, to common.Address, input []byte, block, status uint64) (uint, common.Hash) {
	t.Helper()

	c.mu.Lock()
	index := c.txIndex[block]
	c.txIndex[block]++
	c.mu.Unlock()

	hash := crypto.Keccak256Hash(input, big.NewInt(int64(block)).Bytes(), big.NewInt(int64(index)).Bytes())
	testutil.InsertTx(t, c.DB, testutil.TxRow{To: to, Input: input, Block: block, Index: index, Timestamp: block, Status: status, Hash: hash})
	if status != 1 {
		return 0, hash
	}

	return c.Node.AddTx(block, to, input), hash
}

// AddPolicy records ev, initialized at block.
func (c *Chain) AddPolicy(t *testing.T, ev *relay.RelaySigningPolicyInitialized, block uint64) {
	t.Helper()

	c.log(t, testutil.PolicyEventLog(t, Addresses.Relay, ev, block), 0)

	epoch := uint32(ev.RewardEpochId.Uint64())
	c.mu.Lock()
	c.signStart[epoch] = block
	c.lastEpoch = max(c.lastEpoch, epoch)
	c.mu.Unlock()
}

// AddRegistration records the registration of key for epoch, signed by signer, at
// block; the registration window opens at the earliest one.
func (c *Chain) AddRegistration(t *testing.T, epoch uint32, voter common.Address, key, signer *ecdsa.PrivateKey, block uint64) {
	t.Helper()

	c.log(t, testutil.RegistrationLog(t, Addresses.VoterRegistry, c.ChainID, epoch, voter, key, signer, block), 0)

	c.mu.Lock()
	if opened, ok := c.opened[epoch]; !ok || block < opened {
		c.opened[epoch] = block
	}
	c.mu.Unlock()
}

// SignPolicy records key's signNewSigningPolicy transaction for epoch over hash at block.
func (c *Chain) SignPolicy(t *testing.T, epoch uint32, hash common.Hash, key *ecdsa.PrivateKey, block, status uint64) {
	t.Helper()

	input := testutil.SignNewSigningPolicyInput(t, epoch, hash, key)
	index, txHash := c.tx(t, Addresses.FlareSystemsManager, input, block, status)
	if status != 1 {
		return
	}

	fsmABI, err := system.FlareSystemsManagerMetaData.GetAbi()
	require.NoError(t, err)
	signer := crypto.PubkeyToAddress(key.PublicKey)
	// recorded in the node only: the indexer reads the transaction itself
	c.Node.AddLog(ethtypes.Log{
		Address:     Addresses.FlareSystemsManager,
		Topics:      []common.Hash{fsmABI.Events["SigningPolicySigned"].ID, common.BigToHash(big.NewInt(int64(epoch))), common.BytesToHash(signer[:]), common.BytesToHash(signer[:])},
		BlockNumber: block,
		TxHash:      txHash,
		TxIndex:     index,
	})
}

// SetSignInfo overrides the FSM's record of where epoch's policy was initialized
// and where its signing ended, without recording any event.
func (c *Chain) SetSignInfo(epoch uint32, start, end uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signStart[epoch], c.signEnd[epoch] = start, end
}

// EndSigning records that epoch's signing reached the threshold at block.
func (c *Chain) EndSigning(epoch uint32, block uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signEnd[epoch] = block
}

func (c *Chain) list(extensionID common.Hash, nonce uint64) *pathList {
	k := listKey{extensionID, nonce}
	if c.lists[k] == nil {
		c.lists[k] = &pathList{artifacts: make(map[[32]byte]machinepathmanager.IMachinePathManagerSafeApprovalArtifact)}
	}
	return c.lists[k]
}

// AddPaths records paths appended to the list (extensionID, nonce) at block.
func (c *Chain) AddPaths(t *testing.T, extensionID common.Hash, nonce uint64, paths []cmpaths.IMachinePathManagerMachinePath, block uint64) {
	t.Helper()

	c.log(t, testutil.PathsAddedLog(t, Addresses.MachinePathManager, extensionID, nonce, paths, block, 0, block), 0)

	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.list(extensionID, nonce)
	l.paths = append(l.paths, paths...)
}

// SignList records a governance signature for the list at block.
func (c *Chain) SignList(t *testing.T, extensionID common.Hash, nonce uint64, sig registry.Signature, block, status uint64) {
	t.Helper()

	c.tx(t, Addresses.MachinePathManager, testutil.SignMachinePathListInput(t, extensionID, nonce, sig), block, status)
	if status != 1 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.list(extensionID, nonce)
	l.signatures = append(l.signatures, machinepathmanager.Signature{V: sig.V, R: sig.R, S: sig.S})
}

// ActivateList records the list's activation at block, involving governanceHashes.
func (c *Chain) ActivateList(t *testing.T, extensionID common.Hash, nonce uint64, block uint64, governanceHashes ...common.Hash) {
	t.Helper()

	c.log(t, testutil.ListSignedLog(t, Addresses.MachinePathManager, extensionID, nonce, block), 0)

	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.list(extensionID, nonce)
	l.signed = true
	for _, h := range governanceHashes {
		l.involved = append(l.involved, [32]byte(h))
	}
	c.active[extensionID] = max(c.active[extensionID], nonce)
}

// ApproveBySafe records gov's Safe executing approveMachinePathList for the list at
// block, signed by ownerKeys at safeNonce, and its on-chain confirmation.
func (c *Chain) ApproveBySafe(t *testing.T, gov types.Governance, extensionID common.Hash, nonce uint64, ownerKeys []string, safeNonce uint32, block uint64) {
	t.Helper()

	c.mu.Lock()
	paths := append([]cmpaths.IMachinePathManagerMachinePath{}, c.list(extensionID, nonce).paths...)
	c.mu.Unlock()

	messageHash := testutil.MachinePathListHash(t, c.ChainID, extensionID, nonce, paths)
	calldata, err := types.ApproveMachinePathListCalldata(extensionID, nonce, messageHash)
	require.NoError(t, err)

	safeTx := safe.Tx{To: Addresses.MachinePathManager, Data: calldata, Operation: safe.OperationCall, Nonce: big.NewInt(int64(safeNonce))}
	safeTxHash, err := safeTx.Hash(c.ChainID, gov.Safe)
	require.NoError(t, err)
	signatures := testutil.SignSafeTx(t, safeTxHash, ownerKeys)

	execCalldata, err := safe.EncodeExecTransactionCalldata(safe.ExecTransactionInputs{
		To: Addresses.MachinePathManager, Data: calldata, Operation: safe.OperationCall, Signatures: signatures,
	})
	require.NoError(t, err)
	index, txHash := c.tx(t, gov.Safe, execCalldata, block, 1)

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)
	approved := testutil.LogRow{
		Address:   Addresses.MachinePathManager,
		Event:     managerABI.Events["MachinePathListApproved"],
		Topics:    []common.Hash{extensionID, common.BigToHash(new(big.Int).SetUint64(nonce)), common.BytesToHash(gov.Safe[:])},
		Args:      []any{safeNonce, [][32]byte{}},
		Block:     block,
		Timestamp: block,
		TxHash:    txHash,
	}
	c.log(t, approved, index)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.list(extensionID, nonce).artifacts[gov.Hash] = machinepathmanager.IMachinePathManagerSafeApprovalArtifact{
		SafeNonce:  big.NewInt(int64(safeNonce)),
		Signatures: signatures,
	}
}

func (c *Chain) serveViews(t *testing.T) {
	t.Helper()

	relayABI, err := relay.RelayMetaData.GetAbi()
	require.NoError(t, err)
	fsmABI, err := system.FlareSystemsManagerMetaData.GetAbi()
	require.NoError(t, err)
	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)

	n := c.Node
	n.Handle(Addresses.Relay, relayABI, "signingPolicySetter", func([]any) ([]any, error) {
		return []any{Addresses.FlareSystemsManager}, nil
	})
	n.Handle(Addresses.Relay, relayABI, "lastInitializedRewardEpochData", func([]any) ([]any, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return []any{c.lastEpoch, uint32(0)}, nil
	})
	n.HandleRaw(Addresses.Relay, crypto.Keccak256([]byte("initialRewardEpochId()"))[:4], common.BigToHash(big.NewInt(int64(c.initialEpoch))).Bytes(), nil)

	n.Handle(Addresses.FlareSystemsManager, fsmABI, "getSigningPolicySignInfo", func(args []any) ([]any, error) {
		epoch := uint32(bigArg(args, 0).Uint64())
		c.mu.Lock()
		defer c.mu.Unlock()
		return []any{uint64(0), c.signStart[epoch], uint64(0), c.signEnd[epoch]}, nil
	})
	n.Handle(Addresses.FlareSystemsManager, fsmABI, "getRandomAcquisitionInfo", func(args []any) ([]any, error) {
		epoch := uint32(bigArg(args, 0).Uint64())
		c.mu.Lock()
		defer c.mu.Unlock()
		return []any{uint64(0), uint64(0), uint64(0), c.opened[epoch]}, nil
	})

	noActive := managerABI.Errors["NoActiveMachinePathList"].ID
	n.Handle(Addresses.MachinePathManager, managerABI, "getActiveMachinePathListNonce", func(args []any) ([]any, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		nonce, ok := c.active[common.BigToHash(bigArg(args, 0))]
		if !ok {
			return nil, RevertError(noActive[:4])
		}
		return []any{new(big.Int).SetUint64(nonce)}, nil
	})
	n.Handle(Addresses.MachinePathManager, managerABI, "getMachinePathList", func(args []any) ([]any, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		l := c.list(common.BigToHash(bigArg(args, 0)), bigArg(args, 1).Uint64())
		paths := make([]machinepathmanager.IMachinePathManagerMachinePath, len(l.paths))
		for i, p := range l.paths {
			paths[i] = machinepathmanager.IMachinePathManagerMachinePath{SourceTeeIds: p.SourceTeeIds, DestinationTeeIds: p.DestinationTeeIds}
		}
		return []any{paths, append([][32]byte{}, l.involved...), append([]machinepathmanager.Signature{}, l.signatures...), l.signed}, nil
	})
	n.Handle(Addresses.MachinePathManager, managerABI, "getMachinePathListMessageHash", func(args []any) ([]any, error) {
		extensionID, nonce := common.BigToHash(bigArg(args, 0)), bigArg(args, 1).Uint64()
		c.mu.Lock()
		paths := append([]cmpaths.IMachinePathManagerMachinePath{}, c.list(extensionID, nonce).paths...)
		c.mu.Unlock()
		return []any{[32]byte(testutil.MachinePathListHash(t, c.ChainID, extensionID, nonce, paths))}, nil
	})
	n.Handle(Addresses.MachinePathManager, managerABI, "getMachinePathListSafeApprovalArtifact", func(args []any) ([]any, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		artifact, ok := c.list(common.BigToHash(bigArg(args, 0)), bigArg(args, 1).Uint64()).artifacts[hashArg(args, 2)]
		if !ok {
			artifact = machinepathmanager.IMachinePathManagerSafeApprovalArtifact{SafeNonce: new(big.Int), Signatures: []byte{}}
		}
		return []any{artifact}, nil
	})
}

// bigArg is the unsigned integer call argument i; the ABI decodes uint24 and uint256 as *big.Int.
func bigArg(args []any, i int) *big.Int {
	if b, ok := args[i].(*big.Int); ok {
		return b
	}
	return new(big.Int)
}

func hashArg(args []any, i int) [32]byte {
	h, _ := args[i].([32]byte)
	return h
}
