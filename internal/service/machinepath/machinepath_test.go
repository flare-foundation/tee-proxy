package machinepath

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinepathmanager"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	cmpaths "github.com/flare-foundation/go-flare-common/pkg/tee/structs/machinepath"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/chain/indexer"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

// machinepathPollCount reads teeproxy_machinepath_poll_total for the given result label
// from m's registry, returning 0 if no series with that label exists.
func machinepathPollCount(t *testing.T, m *metrics.Metrics, result string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_machinepath_poll_total" {
			continue
		}
		for _, mc := range f.GetMetric() {
			for _, l := range mc.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return mc.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

// TestPollFetchErrorIncrementsMachinepathPoll guards the LatestSignedList error path: a
// missing logs table (a real database error, not a domain "not found") must increment
// machinepath_poll_total{result="fetch_error"} and leave lastNonce untouched.
func TestPollFetchErrorIncrementsMachinepathPoll(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "machinepath-fetch-error")
	// Deliberately no AutoMigrate: the logs table does not exist, so
	// database.FetchLogsFull returns a genuine "no such table" error.

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	managerAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")
	extensionID := common.HexToHash("0x2a")

	s := NewService(nil, nil, managerAddress, types.Governance{}, extensionID, 14, 0, m)

	// FetchLogsFull retries a genuine DB error under an exponential backoff (up to the
	// package's 15s maxQueryDuration); bound the context so the missing-table error
	// aborts the retry loop quickly instead of running the test for the full window.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	s.poll(ctx, managerSource(db, managerAddress))

	require.Equal(t, float64(1), machinepathPollCount(t, m, "fetch_error"))
	require.Zero(t, s.lastNonce)
}

// TestPollNoChangeIncrementsMachinepathPoll guards the found=false path: an empty logs
// table must increment machinepath_poll_total{result="no_change"} and leave lastNonce
// untouched.
func TestPollNoChangeIncrementsMachinepathPoll(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, "machinepath-no-change")
	require.NoError(t, db.AutoMigrate(&database.Log{}))

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	managerAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")
	extensionID := common.HexToHash("0x2a")

	s := NewService(nil, nil, managerAddress, types.Governance{}, extensionID, 14, 0, m)

	s.poll(context.Background(), managerSource(db, managerAddress))

	require.Equal(t, float64(1), machinepathPollCount(t, m, "no_change"))
	require.Zero(t, s.lastNonce)
}

// signedListFixture is a genuine signed machine-path list indexed as a MachinePathsAdded
// log plus a matching MachinePathListSigned log and governance signMachinePathList
// transaction — everything machinepath.LatestSignedList and SetMachinePathListAction need
// to find and build the SET_MACHINE_PATH_LIST action for nonce.
type signedListFixture struct {
	db             *gorm.DB
	managerAddress common.Address
	extensionID    common.Hash
	chainID        uint64
	nonce          uint64
}

// newSignedListFixture builds a signedListFixture in a fresh in-memory DB named dbName.
func newSignedListFixture(t *testing.T, dbName string) signedListFixture {
	t.Helper()
	return newListFixture(t, dbName, true)
}

// newUnauthorizedListFixture is newSignedListFixture without the governance
// signMachinePathList transaction: the list is activated on chain but carries no
// forwardable authorization evidence.
func newUnauthorizedListFixture(t *testing.T, dbName string) signedListFixture {
	t.Helper()
	return newListFixture(t, dbName, false)
}

func newListFixture(t *testing.T, dbName string, signed bool) signedListFixture {
	t.Helper()

	const chainID = uint64(14)
	managerAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")
	extensionID := common.HexToHash("0x2a")
	nonce := uint64(1)

	managerABI, err := machinepathmanager.MachinePathManagerMetaData.GetAbi()
	require.NoError(t, err)
	addedEvent, ok := managerABI.Events["MachinePathsAdded"]
	require.True(t, ok)
	signMethod, ok := managerABI.Methods["signMachinePathList"]
	require.True(t, ok)

	genPaths := []machinepathmanager.IMachinePathManagerMachinePath{{
		SourceTeeIds:      []common.Address{common.HexToAddress("0x3333333333333333333333333333333333333333")},
		DestinationTeeIds: []common.Address{common.HexToAddress("0x4444444444444444444444444444444444444444")},
	}}
	pathsData, err := addedEvent.Inputs.NonIndexed().Pack(genPaths)
	require.NoError(t, err)

	hashPaths := []cmpaths.IMachinePathManagerMachinePath{{
		SourceTeeIds:      genPaths[0].SourceTeeIds,
		DestinationTeeIds: genPaths[0].DestinationTeeIds,
	}}
	dataHash, err := types.MachinePathListDataHash(extensionID, nonce, hashPaths)
	require.NoError(t, err)
	signHash, err := csigning.NewPayload(csigning.TEEMachinePathList, chainID, dataHash).Hash()
	require.NoError(t, err)
	hash := common.BytesToHash(signHash[:])

	governanceKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	raw, err := crypto.Sign(accounts.TextHash(hash[:]), governanceKey)
	require.NoError(t, err)
	var sig machinepathmanager.Signature
	copy(sig.R[:], raw[:32])
	copy(sig.S[:], raw[32:64])
	sig.V = raw[64] + 27

	extIDBig := new(big.Int).SetBytes(extensionID[:])
	nonceBig := new(big.Int).SetUint64(nonce)
	packedInput, err := signMethod.Inputs.Pack(extIDBig, nonceBig, sig)
	require.NoError(t, err)
	txInput := append(append([]byte{}, signMethod.ID...), packedInput...)

	db, _ := testutil.InMemoryDB(t, dbName)
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	const (
		addedBlock  = uint64(100)
		signedBlock = uint64(110)
		txBlock     = uint64(105)
	)

	nonceTopic := convert.Uint64ToHash(nonce)

	require.NoError(t, db.Create(&database.Log{
		Address:         hex.EncodeToString(managerAddress[:]),
		Data:            hex.EncodeToString(pathsData),
		Topic0:          hex.EncodeToString(addedEvent.ID[:]),
		Topic1:          hex.EncodeToString(extensionID[:]),
		Topic2:          hex.EncodeToString(nonceTopic[:]),
		TransactionHash: "aaaa000000000000000000000000000000000000000000000000000000000001",
		LogIndex:        0,
		Timestamp:       1_000,
		BlockNumber:     addedBlock,
	}).Error)

	testutil.InsertListSigned(t, db, managerAddress, extensionID, nonce, signedBlock)

	if signed {
		require.NoError(t, db.Create(&database.Transaction{
			Hash:        "aaaa000000000000000000000000000000000000000000000000000000000003",
			FunctionSig: hex.EncodeToString(signMethod.ID),
			Input:       hex.EncodeToString(txInput),
			BlockNumber: txBlock,
			ToAddress:   hex.EncodeToString(managerAddress[:]),
			Status:      1,
			Timestamp:   1_500,
		}).Error)
	}

	return signedListFixture{db: db, managerAddress: managerAddress, extensionID: extensionID, chainID: chainID, nonce: nonce}
}

// TestPollNoAuthorizationIncrementsMachinepathPoll guards the ErrNoAuthorization path: an
// activated list with no authorization evidence (no signMachinePathList transaction, no
// Safe approval) must increment machinepath_poll_total{result="no_authorization"} — not
// "build_error" — and leave lastNonce unchanged so the next poll retries the same list.
func TestPollNoAuthorizationIncrementsMachinepathPoll(t *testing.T) {
	fx := newUnauthorizedListFixture(t, "machinepath-no-authorization")

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	s := NewService(nil, nil, fx.managerAddress, types.Governance{}, fx.extensionID, fx.chainID, 0, m)

	s.poll(context.Background(), managerSource(fx.db, fx.managerAddress))

	require.Equal(t, float64(1), machinepathPollCount(t, m, "no_authorization"))
	require.Equal(t, float64(0), machinepathPollCount(t, m, "build_error"))
	require.Zero(t, s.lastNonce, "lastNonce must not advance until the node confirms the action")
}

// TestPollEnqueueErrorIncrementsMachinepathPoll guards the aq.Enqueue error path: a genuine
// signed list whose action builds successfully (a valid MachinePathsAdded log plus a
// recoverable governance signature over a signMachinePathList transaction) but whose
// enqueue fails (Redis unreachable) must increment
// machinepath_poll_total{result="enqueue_error"} and leave lastNonce unchanged so the next
// poll retries the same list.
func TestPollEnqueueErrorIncrementsMachinepathPoll(t *testing.T) {
	fx := newSignedListFixture(t, "machinepath-enqueue-error")

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c, time.Hour, nil)

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	s := NewService(aq, nil, fx.managerAddress, types.Governance{}, fx.extensionID, fx.chainID, 0, m)

	// Redis is gone by the time poll reaches Enqueue, so the action (which must build
	// successfully first) fails only at the enqueue step.
	mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.poll(ctx, managerSource(fx.db, fx.managerAddress))

	require.Equal(t, float64(1), machinepathPollCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), machinepathPollCount(t, m, "fetch_error"))
	require.Equal(t, float64(0), machinepathPollCount(t, m, "build_error"))
	require.Equal(t, float64(0), machinepathPollCount(t, m, "confirmed"))
	require.Zero(t, s.lastNonce, "lastNonce must not advance until the node confirms the action")
}

// TestPollWaitErrorIncrementsMachinepathPoll guards the confirmation-wait error path: a
// successfully enqueued action whose node confirmation never arrives (the wait runs out
// the ctx deadline) must increment machinepath_poll_total{result="wait_error"} and leave
// lastNonce unchanged so the next poll retries the same list.
func TestPollWaitErrorIncrementsMachinepathPoll(t *testing.T) {
	fx := newSignedListFixture(t, "machinepath-wait-error")

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	s := NewService(aq, rs, fx.managerAddress, types.Governance{}, fx.extensionID, fx.chainID, 0, m)

	// No fake node ever responds, so the confirmation wait ends on the ctx deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	s.poll(ctx, managerSource(fx.db, fx.managerAddress))

	require.Equal(t, float64(1), machinepathPollCount(t, m, "wait_error"))
	require.Equal(t, float64(0), machinepathPollCount(t, m, "enqueue_error"))
	require.Equal(t, float64(0), machinepathPollCount(t, m, "confirmed"))
	require.Zero(t, s.lastNonce, "lastNonce must not advance until the node confirms the action")
}

// TestPollConfirmedAdvancesLastNonce guards the success path that none of the other poll
// tests reach: once the TEE node confirms the SET_MACHINE_PATH_LIST action (Status 1),
// poll must record "confirmed" and advance lastNonce so the next poll does not resubmit
// the same list.
func TestPollConfirmedAdvancesLastNonce(t *testing.T) {
	fx := newSignedListFixture(t, "machinepath-confirmed")

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	n := storage.NewNotifier(c)
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	s := NewService(aq, rs, fx.managerAddress, types.Governance{}, fx.extensionID, fx.chainID, 0, m)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.poll(ctx, managerSource(fx.db, fx.managerAddress))
	}()

	var action *types.Action
	require.Eventually(t, func() bool {
		d, err := aq.Dequeue(ctx, processorutils.Direct)
		if err != nil {
			return false
		}
		action = d.Action
		return true
	}, 3*time.Second, 10*time.Millisecond, "poll did not enqueue the SET_MACHINE_PATH_LIST action")

	require.NoError(t, rs.StoreResponse(ctx, &types.ActionResponse{
		Result: types.ActionResult{
			ID:            action.Data.ID,
			SubmissionTag: action.Data.SubmissionTag,
			Status:        1,
		},
	}))

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("poll did not return after the node confirmed the action")
	}

	require.Equal(t, float64(1), machinepathPollCount(t, m, "confirmed"))
	require.Equal(t, fx.nonce, s.lastNonce)
}

// TestPollPicksHighestSignedNonce pins the selection rule: among several lists signed
// after lastNonce, poll submits the highest nonce, not the most recently signed one.
func TestPollPicksHighestSignedNonce(t *testing.T) {
	db, _ := testutil.InMemoryDB(t, t.Name())
	require.NoError(t, db.AutoMigrate(&database.Log{}, &database.Transaction{}))

	const (
		chainID = uint64(14)
		highest = uint64(9)
	)
	managerAddress := common.HexToAddress("0x1111111111111111111111111111111111111111")
	extensionID := common.HexToHash("0x2a")

	paths := []cmpaths.IMachinePathManagerMachinePath{testutil.MachinePath(common.HexToAddress("0x33"), common.HexToAddress("0x44"))}
	testutil.InsertPathsAdded(t, db, managerAddress, extensionID, highest, paths, 100, 0, 100)
	governanceKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	sig := testutil.GovernanceSignature(t, testutil.MachinePathListHash(t, chainID, extensionID, highest, paths), governanceKey)
	testutil.InsertTx(t, db, testutil.TxRow{To: managerAddress, Input: testutil.SignMachinePathListInput(t, extensionID, highest, sig), Block: 101, Timestamp: 101, Status: 1})

	// only the highest nonce has paths; picking any other fails the build
	testutil.InsertListSigned(t, db, managerAddress, extensionID, 3, 102)
	testutil.InsertListSigned(t, db, managerAddress, extensionID, highest, 103)
	testutil.InsertListSigned(t, db, managerAddress, extensionID, 5, 104)
	testutil.InsertListSigned(t, db, managerAddress, extensionID, 1, 105) // not after lastNonce

	mr := miniredis.RunT(t)
	c := storage.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c, time.Hour, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), time.Hour, time.Hour)

	m := metrics.New(metrics.Config{Enable: true, Node: true})
	s := NewService(aq, rs, managerAddress, types.Governance{}, extensionID, chainID, 2, m)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.poll(ctx, managerSource(db, managerAddress))
	}()

	var action *types.Action
	require.Eventually(t, func() bool {
		d, err := aq.Dequeue(ctx, processorutils.Direct)
		if err != nil {
			return false
		}
		action = d.Action
		return true
	}, 3*time.Second, 10*time.Millisecond, "poll did not enqueue the SET_MACHINE_PATH_LIST action")

	var di types.DirectInstruction
	require.NoError(t, json.Unmarshal(action.Data.Message, &di))
	var req types.SetMachinePathListRequest
	require.NoError(t, json.Unmarshal(di.Message, &req))
	require.Equal(t, highest, req.Nonce)

	require.NoError(t, rs.StoreResponse(ctx, &types.ActionResponse{
		Result: types.ActionResult{ID: action.Data.ID, SubmissionTag: action.Data.SubmissionTag, Status: 1},
	}))

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("poll did not return after the node confirmed the action")
	}

	require.Equal(t, highest, s.lastNonce)
}

// managerSource reads the test fixtures in db through the indexer backend.
func managerSource(db *gorm.DB, manager common.Address) *indexer.Source {
	return indexer.New(db, config.Addresses{MachinePathManager: manager}, database.SyncParams{})
}
