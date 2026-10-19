package policy_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	cpolicy "github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flare-foundation/tee-proxy/internal/chain/indexer"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/policy"
)

const rolloverChainID = 14

var rolloverAddresses = config.Addresses{
	FlareSystemsManager: common.HexToAddress("0xf5f5"),
	Relay:               common.HexToAddress("0x2e1a"),
	VoterRegistry:       common.HexToAddress("0x7e61"),
}

func newChainDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, _ := testutil.InMemoryDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}, &database.State{}))

	return db
}

func source(db *gorm.DB) *indexer.Source {
	return indexer.New(db, rolloverAddresses, database.SyncParams{})
}

func newKeys(t *testing.T, n int) []*ecdsa.PrivateKey {
	t.Helper()

	keys := make([]*ecdsa.PrivateKey, n)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
	}

	return keys
}

// identity is an arbitrary voter identity address paired with a signing-policy key.
func identity(k *ecdsa.PrivateKey) common.Address {
	a := crypto.PubkeyToAddress(k.PublicKey)
	return common.BytesToAddress(crypto.Keccak256(a[:]))
}

// addPolicy indexes epoch's policy at block and a valid registration for every key.
func addPolicy(t *testing.T, db *gorm.DB, epoch uint32, keys []*ecdsa.PrivateKey, weights []uint16, threshold uint16, block uint64) *relay.RelaySigningPolicyInitialized {
	t.Helper()

	ev := testutil.PolicyEvent(epoch, keys, weights, threshold)
	testutil.InsertPolicyEvent(t, db, rolloverAddresses.Relay, ev, block)
	for _, k := range keys {
		testutil.InsertRegistration(t, db, rolloverAddresses.VoterRegistry, rolloverChainID, epoch, identity(k), k, k, block-1)
	}

	return ev
}

func pubKeys(keys []*ecdsa.PrivateKey) []types.PublicKey {
	out := make([]types.PublicKey, len(keys))
	for i, k := range keys {
		out[i] = types.PubKeyToStruct(&k.PublicKey)
	}
	return out
}

// decodeDirect unpacks the request carried by a direct action.
func decodeDirect(t *testing.T, action *types.Action, req any) {
	t.Helper()

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	require.NoError(t, json.Unmarshal(di.Message, req))
}

func TestInitializePolicyActionPicksOldestOfN(t *testing.T) {
	db := newChainDB(t)
	keys := newKeys(t, 3)
	weights := []uint16{100, 200, 300}

	events := map[uint32]*relay.RelaySigningPolicyInitialized{}
	for i, epoch := range []uint32{5, 6, 7} {
		events[epoch] = addPolicy(t, db, epoch, keys, weights, 300, uint64(100*(i+1)))
	}

	tests := []struct {
		name       string
		offset     int
		wantEpoch  uint32
		wantOffset int
	}{
		{name: "offset within history", offset: 1, wantEpoch: 6, wantOffset: 1},
		{name: "offset beyond history falls back to oldest", offset: 5, wantEpoch: 5, wantOffset: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action, p, offset, err := policy.InitializePolicyAction(context.Background(), source(db), rolloverAddresses, tc.offset, big.NewInt(rolloverChainID))
			require.NoError(t, err)
			require.Equal(t, tc.wantEpoch, p.RewardEpochID)
			require.Equal(t, tc.wantOffset, offset)

			var req types.InitializePolicyRequest
			decodeDirect(t, action, &req)
			require.Equal(t, events[tc.wantEpoch].SigningPolicyBytes, req.InitialPolicyBytes)
			require.Equal(t, pubKeys(keys), req.PublicKeys)
		})
	}
}

func TestInitializePolicyActionRegistrationRules(t *testing.T) {
	const epoch = 5

	tests := []struct {
		name       string
		register   func(t *testing.T, db *gorm.DB, keys []*ecdsa.PrivateKey)
		wantReason string
	}{
		{
			name: "missing registration",
			register: func(t *testing.T, db *gorm.DB, keys []*ecdsa.PrivateKey) {
				t.Helper()
				for _, k := range keys[:2] {
					testutil.InsertRegistration(t, db, rolloverAddresses.VoterRegistry, rolloverChainID, epoch, identity(k), k, k, 90)
				}
			},
			wantReason: "indexer",
		},
		{
			name: "duplicate registration",
			register: func(t *testing.T, db *gorm.DB, keys []*ecdsa.PrivateKey) {
				t.Helper()
				for _, k := range keys {
					testutil.InsertRegistration(t, db, rolloverAddresses.VoterRegistry, rolloverChainID, epoch, identity(k), k, k, 90)
				}
				testutil.InsertRegistration(t, db, rolloverAddresses.VoterRegistry, rolloverChainID, epoch, identity(keys[0]), keys[0], keys[0], 91)
			},
			wantReason: "indexer",
		},
		{
			name: "signed by another key",
			register: func(t *testing.T, db *gorm.DB, keys []*ecdsa.PrivateKey) {
				t.Helper()
				for _, k := range keys[:2] {
					testutil.InsertRegistration(t, db, rolloverAddresses.VoterRegistry, rolloverChainID, epoch, identity(k), k, k, 90)
				}
				testutil.InsertRegistration(t, db, rolloverAddresses.VoterRegistry, rolloverChainID, epoch, identity(keys[2]), keys[2], keys[0], 90)
			},
			wantReason: "pubkey_mismatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newChainDB(t)
			keys := newKeys(t, 3)
			testutil.InsertPolicyEvent(t, db, rolloverAddresses.Relay, testutil.PolicyEvent(epoch, keys, []uint16{1, 1, 1}, 1), 100)
			tc.register(t, db, keys)

			_, _, _, err := policy.InitializePolicyAction(context.Background(), source(db), rolloverAddresses, 0, big.NewInt(rolloverChainID))
			require.Error(t, err)
			require.Equal(t, tc.wantReason, policy.UpdateFailureReason(err))
		})
	}
}

// rollover is an indexed epoch 6 → 7 rollover: the active policy, the new policy's
// event at newPolicyBlock, and registrations for the new voters.
type rollover struct {
	db         *gorm.DB
	active     *cpolicy.SigningPolicy
	activeKeys []*ecdsa.PrivateKey // weights 300, 300, 600; threshold 500
	newKeys    []*ecdsa.PrivateKey
	hash       common.Hash // what voters sign for epoch 7
}

const newPolicyBlock = 100

func newRollover(t *testing.T) rollover {
	t.Helper()

	db := newChainDB(t)
	activeKeys := newKeys(t, 3)
	active, err := cpolicy.NewSigningPolicy(testutil.PolicyEvent(6, activeKeys, []uint16{300, 300, 600}, 500), nil)
	require.NoError(t, err)

	nKeys := newKeys(t, 2)
	ev := addPolicy(t, db, 7, nKeys, []uint16{1, 1}, 1, newPolicyBlock)
	next, err := cpolicy.NewSigningPolicy(ev, nil)
	require.NoError(t, err)

	return rollover{
		db:         db,
		active:     active,
		activeKeys: activeKeys,
		newKeys:    nKeys,
		hash:       common.BytesToHash(next.ChainBoundHash(rolloverChainID)),
	}
}

func (r rollover) sign(t *testing.T, key *ecdsa.PrivateKey, block, timestamp, status uint64) {
	t.Helper()

	testutil.InsertTx(t, r.db, testutil.TxRow{
		To:        rolloverAddresses.FlareSystemsManager,
		Input:     testutil.SignNewSigningPolicyInput(t, 7, r.hash, key),
		Block:     block,
		Timestamp: timestamp,
		Status:    status,
	})
}

func (r rollover) update(t *testing.T) (*types.Action, *cpolicy.SigningPolicy, error) {
	t.Helper()

	event, found, err := policy.FetchSigningPolicyEvent(context.Background(), source(r.db), 7)
	require.NoError(t, err)
	require.True(t, found)

	return policy.UpdatePolicyAction(context.Background(), source(r.db), rolloverAddresses, event, r.active, big.NewInt(rolloverChainID))
}

func TestUpdatePolicyActionCollectsSignatures(t *testing.T) {
	r := newRollover(t)
	a, b, heavy := r.activeKeys[0], r.activeKeys[1], r.activeKeys[2]
	stranger := newKeys(t, 1)[0]

	const head = 120
	testutil.InsertState(t, r.db, head, uint64(time.Now().Unix()))

	// Ordered by timestamp; heavy alone exceeds the threshold, so any of its
	// signatures slipping through would end collection early.
	r.sign(t, heavy, newPolicyBlock-1, 1, 1) // before the window
	r.sign(t, heavy, 101, 2, 0)              // reverted
	r.sign(t, a, 102, 3, 1)
	r.sign(t, a, 103, 4, 1)        // duplicate signer
	r.sign(t, stranger, 104, 5, 1) // not in the active policy
	// signs another epoch
	testutil.InsertTx(t, r.db, testutil.TxRow{
		To:        rolloverAddresses.FlareSystemsManager,
		Input:     testutil.SignNewSigningPolicyInput(t, 8, r.hash, heavy),
		Block:     105,
		Timestamp: 6,
		Status:    1,
	})
	r.sign(t, heavy, head+1, 7, 1) // past the indexed head
	r.sign(t, b, head, 8, 1)       // at the head: inclusive

	action, next, err := r.update(t)
	require.NoError(t, err)
	require.Equal(t, uint32(7), next.RewardEpochID)

	var req types.UpdatePolicyRequest
	decodeDirect(t, action, &req)
	require.Equal(t, [][]byte{
		testutil.SerializedSignature(testutil.PolicySignature(t, r.hash, a)),
		testutil.SerializedSignature(testutil.PolicySignature(t, r.hash, b)),
	}, req.NewPolicy.Signatures)
	require.Equal(t, next.RawBytes(), req.NewPolicy.PolicyBytes)
	require.Equal(t, pubKeys(r.newKeys), req.PublicKeys)
}

func TestUpdatePolicyActionDeadline(t *testing.T) {
	r := newRollover(t)
	// the head is already past the 30m collection deadline
	testutil.InsertState(t, r.db, 120, uint64(time.Now().Add(time.Hour).Unix()))
	r.sign(t, r.activeKeys[0], 102, 1, 1)

	_, _, err := r.update(t)
	require.ErrorIs(t, err, policy.ErrDeadlineExceeded)
	require.Equal(t, "sig_deadline", policy.UpdateFailureReason(err))
}

func TestUpdatePolicyActionNotConsecutive(t *testing.T) {
	r := newRollover(t)
	stale, err := cpolicy.NewSigningPolicy(testutil.PolicyEvent(5, r.activeKeys, []uint16{300, 300, 600}, 500), nil)
	require.NoError(t, err)
	r.active = stale

	_, _, err = r.update(t)
	require.Equal(t, "not_consecutive", policy.UpdateFailureReason(err))
}
