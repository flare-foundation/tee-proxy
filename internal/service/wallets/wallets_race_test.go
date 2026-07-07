package wallets

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/stretchr/testify/require"
)

// makeKeyGenActionResult builds a valid KEY_GENERATE ActionResult for the given
// walletID and keyID. It is a test helper used to seed and drive Service state.
func makeKeyGenActionResult(t *testing.T, walletID common.Hash, keyID uint64) *types.ActionResult {
	t.Helper()

	adminKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	teeKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	teeID := crypto.PubkeyToAddress(teeKey.PublicKey)

	actionMsg := wallet.IWalletKeyManagerKeyGenerate{
		SigningAlgo: wallets.XRPSignAlgo,
		KeyType:     wallets.XRPType,
		TeeId:       teeID,
		WalletId:    walletID,
		KeyId:       keyID,
		ConfigConstants: wallet.IWalletKeyManagerKeyConfigConstants{
			AdminsPublicKeys: []wallet.PublicKey{{
				X: common.BigToHash(adminKey.X),
				Y: common.BigToHash(adminKey.Y),
			}},
			AdminsThreshold:    1,
			Cosigners:          []common.Address{},
			CosignersThreshold: 0,
		},
	}

	wal, err := wallets.GenerateNewKey(actionMsg)
	require.NoError(t, err)

	kep := wal.KeyExistenceProof(teeID)

	encoded, err := structs.Encode(wallet.KeyExistenceStructArg, kep)
	require.NoError(t, err)

	sig, err := crypto.Sign(accounts.TextHash(crypto.Keccak256(encoded)), teeKey)
	require.NoError(t, err)

	data, err := json.Marshal(wallets.SignedKeyExistenceProof{
		KeyExistence: encoded,
		Signature:    sig,
	})
	require.NoError(t, err)

	return &types.ActionResult{
		Status:    1,
		OPType:    op.Wallet.Hash(),
		OPCommand: op.KeyGenerate.Hash(),
		Data:      data,
	}
}

// TestWalletInfoDataRace detects the data race in which WalletInfo and KeyData return
// pointers into the Service's map-owned KeyData struct, and a concurrent updateOrAddKey
// call writes to that same struct's Info field.
//
// Run with: go test -race ./internal/service/wallets/...
func TestWalletInfoDataRace(t *testing.T) {
	walletID := common.BytesToHash([]byte("race-wallet"))
	svc := NewService(nil, nil, nil, nil, time.Hour, nil)

	aResult := makeKeyGenActionResult(t, walletID, 0)
	_, _, err := svc.update(aResult)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup

	// Writer: repeatedly calls updateOrAddKey, which writes keyData.Info = *info
	// to the struct stored in s.Keys.
	wg.Go(func() {
		for ctx.Err() == nil {
			_, _ = svc.updateOrAddKey(aResult)
		}
	})

	// Reader 1: calls WalletInfo and reads through the returned *KeyExistence
	// pointer after WalletInfo has released its lock.
	wg.Go(func() {
		for ctx.Err() == nil {
			wi, err := svc.WalletInfo(walletID)
			if err == nil {
				_ = wi.ConfigConstants.CosignersThreshold
				_ = wi.ConfigConstants.AdminsThreshold
			}
		}
	})

	// Reader 2: calls KeyData and reads through the returned *KeyData pointer
	// after KeyData has released its lock.
	wg.Go(func() {
		for ctx.Err() == nil {
			kd, err := svc.KeyData(walletID, 0)
			if err == nil {
				_ = kd.Info.ConfigConstants.CosignersThreshold
				_ = kd.Info.KeyType
			}
		}
	})

	wg.Wait()
}

// TestWalletInfoDeadlock detects the deadlock caused by WalletInfo holding an RLock
// and then calling KeyData, which tries to acquire a second RLock. When a writer is
// concurrently blocked on Lock(), new RLock calls block too, causing a deadlock.
//
// The test runs WalletInfo in a tight loop against a concurrent writer. Before the
// fix, WalletInfo deadlocks and never returns; the 2-second overall timeout fires
// and the test fails. After the fix (no nested RLock), all calls complete.
func TestWalletInfoDeadlock(t *testing.T) {
	walletID := common.BytesToHash([]byte("deadlock-wallet"))
	svc := NewService(nil, nil, nil, nil, time.Hour, nil)

	aResult := makeKeyGenActionResult(t, walletID, 0)
	_, _, err := svc.update(aResult)
	require.NoError(t, err)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	// Writer goroutine: locks and unlocks in a tight loop, creating the write-lock
	// contention that can land between WalletInfo's outer RLock and KeyData's inner RLock.
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				svc.Lock()
				svc.Unlock()
			}
		}
	}()

	// Reader goroutine: calls WalletInfo in a loop and signals each completion.
	callDone := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = svc.WalletInfo(walletID)
			select {
			case callDone <- struct{}{}:
			default:
			}
		}
	}()

	timeout := time.After(2 * time.Second)
	for i := range 10_000 {
		select {
		case <-callDone:
		case <-timeout:
			t.Fatalf("WalletInfo deadlocked after %d successful calls", i)
		}
	}
}
