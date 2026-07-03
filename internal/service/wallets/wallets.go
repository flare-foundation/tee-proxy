package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	pkgwallets "github.com/flare-foundation/tee-proxy/pkg/wallets"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
)

const (
	keyInfoResponseTimeout  = 3 * time.Minute
	keyProofResponseTimeout = 3 * time.Minute
	keyProofBatchSize       = 100
)

var (
	errKeyProofNotFound    = fmt.Errorf("%w: key proof not found", status.HTTP[404])
	errWalletNotFound      = errors.New("wallet not found")
	errKeyDataNotFound     = fmt.Errorf("%w: key data not found", status.HTTP[404])
	errKeyUpdate           = errors.New("key update action not successful")
	errInvalidActionResult = errors.New("invalid action result status")
	errInvalidOpType       = errors.New("invalid action opType")
	errInvalidOpCommand    = errors.New("invalid action opCommand")
)

// IDPair identifies a key by its (walletID, keyID) pair.
type IDPair = wallets.KeyIDPair

// Service is the proxy-side mirror of tee-node wallet state: cached key proofs and a
// pipeline of backups indexed for retrieval by clients.
type Service struct {
	KeysForWallet map[common.Hash][]uint64       // slice of keyIDs per walletID
	Keys          map[IDPair]*pkgwallets.KeyData // key data per IDPair

	index   storage.Storage[common.Hash]                // latest backup ID hash per IDPair
	backups storage.Storage[*wallets.TEEBackupResponse] // backups per backupIDHash

	aq        *queue.ActionQueues
	rs        *result.ResultStorage
	backupTTL time.Duration

	metrics *metrics.Metrics

	// syncing serialises sync() across overlapping triggers so the event loop
	// keeps draining other channels while a long sync is in progress.
	syncing atomic.Bool

	sync.RWMutex
}

// NewService wires the wallet service to its action queue, result storage, and backup stores.
func NewService(aq *queue.ActionQueues, rs *result.ResultStorage, index storage.Storage[common.Hash], backups storage.Storage[*wallets.TEEBackupResponse], backupTTL time.Duration, m *metrics.Metrics) *Service {
	return &Service{
		KeysForWallet: make(map[common.Hash][]uint64),
		Keys:          make(map[IDPair]*pkgwallets.KeyData),

		index:   index,
		backups: backups,

		aq:        aq,
		rs:        rs,
		backupTTL: backupTTL,
		metrics:   m,
	}
}

// RunUpdateInfo runs the wallet service's event loop until ctx is cancelled.
// Multiplexes periodic sync triggers, key-update and backup results, and the
// epoch-rollover backup trigger onto a single goroutine.
func (s *Service) RunUpdateInfo(ctx context.Context, walletSyncTrigger, backupTrigger <-chan bool, keyActions <-chan *types.ActionResult, backups <-chan *types.ActionResult) {
	for {
		select {
		case <-ctx.Done():
			logger.Info("wallet event loop exiting")
			return
		case <-walletSyncTrigger:
			// Run sync in its own goroutine so the loop keeps draining keyActions,
			// backups, and backupTrigger while sync waits on the tee-node (up to
			// minutes per batch). syncing guards against overlapping syncs.
			if !s.syncing.CompareAndSwap(false, true) {
				logger.Debug("wallet sync skipped: already in progress")
				continue
			}
			go func() {
				defer s.syncing.Store(false)
				logger.Debug("wallet sync start")
				if err := s.sync(ctx); err != nil {
					logger.Errorf("wallet sync: %v", err)
					return
				}
				logger.Debug("wallet sync done")
			}()
		case keyAction := <-keyActions:
			logger.Debug("wallet key update start")
			id, added, err := s.update(keyAction)
			if err != nil {
				logger.Errorf("wallet key update: %v", err)
				continue
			}

			action := "added"
			if !added {
				action = "removed"
			}
			logger.Debugf("walletID: %v keyID: %d %s", id.WalletID, id.KeyID, action)

			if added {
				go func() {
					logger.Debugf("starting backup for %v", id)

					err := s.initiateBackup(ctx, id)
					if err != nil {
						logger.Errorf("making backup for %v: %v", id, err)
					}
					logger.Debugf("backup done for %v", id)
				}()
			}
		case backupResult := <-backups:
			logger.Debug("storing backup result")
			err := s.createNewBackup(ctx, backupResult)
			if err != nil {
				logger.Errorf("storing backup result: %v", err)
				continue
			}

		case <-backupTrigger:
			logger.Debug("backups triggered")
			err := s.InitiateBackups(ctx)
			if err != nil {
				logger.Errorf("triggering backups: %v", err)
				continue
			}
			logger.Debug("backups enqueued")
		}
	}
}

// WalletsInfo returns summary information about all stored wallets and keys.
func (s *Service) WalletsInfo() WalletsInfoResponse {
	s.RLock()
	defer s.RUnlock()

	keys := make([]IDPair, 0, len(s.Keys))
	for id := range s.Keys {
		keys = append(keys, id)
	}

	return WalletsInfoResponse{
		Wallets: len(s.KeysForWallet),
		Keys:    len(s.Keys),
		Pairs:   keys,
	}
}

// WalletsInfoResponse contains summary information about stored wallets.
type WalletsInfoResponse struct {
	Wallets int      `json:"wallets"`
	Keys    int      `json:"keys"`
	Pairs   []IDPair `json:"pairs"`
}

// KeyProof returns the signed key existence proof for the given wallet/key pair.
//
// The returned pointer is safe to use after the lock is released because
// SignedKeyExistenceProof values are immutable once created: updateOrAddKey replaces
// the Proof field with a new pointer rather than writing through the existing one.
// If that invariant ever changes this must be updated to return a copy.
func (s *Service) KeyProof(walletID common.Hash, keyID uint64) (*wallets.SignedKeyExistenceProof, error) {
	s.RLock()
	defer s.RUnlock()
	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, errKeyProofNotFound
	}

	return info.Proof, nil
}

// WalletInfo returns the KeyExistence of any one key under walletID, used by callers
// that need wallet-level config (admins, cosigners) and don't care which key answers.
func (s *Service) WalletInfo(walletID common.Hash) (*pkgwallets.KeyExistence, error) {
	s.RLock()
	defer s.RUnlock()

	keys, exists := s.KeysForWallet[walletID]
	if !exists || len(keys) == 0 {
		return nil, errWalletNotFound
	}

	id := IDPair{WalletID: walletID, KeyID: keys[0]}
	data, exists := s.keyDataLocked(id)
	if !exists {
		return nil, errKeyDataNotFound
	}

	// Copy while the lock is held so the caller cannot race with a concurrent
	// updateOrAddKey that writes keyData.Info = *info to the same struct.
	info := data.Info
	return &info, nil
}

// KeyData returns a copy of the cached key record for the given pair, or errKeyDataNotFound.
func (s *Service) KeyData(walletID common.Hash, keyID uint64) (*pkgwallets.KeyData, error) {
	s.RLock()
	defer s.RUnlock()

	id := IDPair{WalletID: walletID, KeyID: keyID}
	data, exists := s.keyDataLocked(id)
	if !exists {
		return nil, errKeyDataNotFound
	}

	kd := *data
	return &kd, nil
}

// keyDataLocked returns the KeyData entry for id. Caller must hold at least s.RLock.
func (s *Service) keyDataLocked(id IDPair) (*pkgwallets.KeyData, bool) {
	data, exists := s.Keys[id]
	return data, exists
}

// sync enqueues KEY_INFO action, compares the returned key list with locally cached keys,
// fetches proofs via KEY_PROOF in batches for keys that are missing locally or whose nonce
// has changed, and removes stale entries.
func (s *Service) sync(ctx context.Context) error {
	remoteKeys, err := s.fetchKeyInfo(ctx)
	if err != nil {
		return fmt.Errorf("fetching key info: %w", err)
	}

	// Snapshot local keys now so removeStaleKeys won't evict keys the event loop adds
	// during the sync window; those are newer than this remote view.
	localAtStart := s.localKeySet()

	toFetch := s.keysNeedingProof(remoteKeys)

	for batch := range slices.Chunk(toFetch, keyProofBatchSize) {
		proofs, err := s.fetchKeyProofs(ctx, batch)
		if err != nil {
			return fmt.Errorf("fetching key proofs: %w", err)
		}

		// Lock is dropped between batches so the event loop keeps draining. Safe today
		// because event-loop writers don't touch IDPairs in toFetch; a RESTORE landing
		// here would lose to a stale batch write but self-heals on the next sync.
		s.Lock()
		for _, proof := range proofs {
			info, err := parseKeyExistenceProof(proof)
			if err != nil {
				s.Unlock()
				return fmt.Errorf("parsing key proof: %w", err)
			}

			id := IDPair{WalletID: info.WalletID, KeyID: info.KeyID}
			s.Keys[id] = &pkgwallets.KeyData{Info: *info, Proof: proof}

			if !slices.Contains(s.KeysForWallet[info.WalletID], info.KeyID) {
				s.KeysForWallet[info.WalletID] = append(s.KeysForWallet[info.WalletID], info.KeyID)
			}
		}
		s.Unlock()
	}

	s.removeStaleKeys(remoteKeys, localAtStart)

	return nil
}

// localKeySet returns the set of currently-cached key IDs.
func (s *Service) localKeySet() map[IDPair]bool {
	s.RLock()
	defer s.RUnlock()

	set := make(map[IDPair]bool, len(s.Keys))
	for id := range s.Keys {
		set[id] = true
	}

	return set
}

// fetchKeyInfo sends a KEY_INFO action and returns the list of key infos from the tee-node.
// Routes through ResultStorage so the action ID disambiguates concurrent or stale responses.
func (s *Service) fetchKeyInfo(ctx context.Context) ([]types.KeyInfo, error) {
	action, err := keysInfoAction()
	if err != nil {
		return nil, fmt.Errorf("creating key info action: %w", err)
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	response, err := s.rs.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, keyInfoResponseTimeout)
	s.metrics.ObserveNodeWait("wallet_key_info", time.Since(start), err)
	if err != nil {
		return nil, err
	}

	return parseKeyInfoResult(&response.Result)
}

// keysNeedingProof returns the key ID pairs from remote that are not in the local cache
// or whose nonce differs from the cached value.
func (s *Service) keysNeedingProof(remote []types.KeyInfo) []IDPair {
	s.RLock()
	defer s.RUnlock()

	toFetch := make([]IDPair, 0)
	for _, info := range remote {
		id := IDPair{WalletID: info.WalletID, KeyID: info.KeyID}
		storedKey, exists := s.Keys[id]
		if !exists {
			toFetch = append(toFetch, id)
			continue
		}
		if storedKey.Info.Nonce == nil || storedKey.Info.Nonce.Uint64() != info.Nonce {
			toFetch = append(toFetch, id)
		}
	}

	return toFetch
}

// removeStaleKeys removes locally cached keys that are absent from remote, except those
// not in localAtStart: keys added during the sync window are preserved, not evicted.
func (s *Service) removeStaleKeys(remote []types.KeyInfo, localAtStart map[IDPair]bool) {
	remoteSet := make(map[IDPair]bool, len(remote))
	for _, info := range remote {
		remoteSet[IDPair{WalletID: info.WalletID, KeyID: info.KeyID}] = true
	}

	s.Lock()
	defer s.Unlock()

	for id := range s.Keys {
		if remoteSet[id] || !localAtStart[id] {
			continue
		}

		delete(s.Keys, id)

		s.KeysForWallet[id.WalletID] = slices.DeleteFunc(s.KeysForWallet[id.WalletID], func(k uint64) bool {
			return k == id.KeyID
		})
		if len(s.KeysForWallet[id.WalletID]) == 0 {
			delete(s.KeysForWallet, id.WalletID)
		}
	}
}

// fetchKeyProofs sends a KEY_PROOF action for the given batch of key IDs and waits for the response.
func (s *Service) fetchKeyProofs(ctx context.Context, batch []IDPair) ([]*wallets.SignedKeyExistenceProof, error) {
	msg, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}

	action, err := queue.PrepareDirectAction(op.Get, op.KeyProof, msg)
	if err != nil {
		return nil, err
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	response, err := s.rs.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, keyProofResponseTimeout)
	s.metrics.ObserveNodeWait("wallet_key_proof", time.Since(start), err)
	if err != nil {
		return nil, err
	}

	return parseKeyProofResult(&response.Result)
}

// parseKeyInfoResult parses a KEY_INFO response into a list of key infos.
func parseKeyInfoResult(r *types.ActionResult) ([]types.KeyInfo, error) {
	if r.Status != 1 {
		return nil, errInvalidActionResult
	}
	if r.OPType != op.Get.Hash() {
		return nil, errInvalidOpType
	}
	if r.OPCommand != op.KeyInfo.Hash() {
		return nil, errInvalidOpCommand
	}

	var infos []types.KeyInfo
	err := json.Unmarshal(r.Data, &infos)
	if err != nil {
		return nil, err
	}

	return infos, nil
}

// parseKeyProofResult parses a KEY_PROOF response into signed existence proofs.
func parseKeyProofResult(r *types.ActionResult) ([]*wallets.SignedKeyExistenceProof, error) {
	if r.Status != 1 {
		return nil, errInvalidActionResult
	}
	if r.OPType != op.Get.Hash() {
		return nil, errInvalidOpType
	}
	if r.OPCommand != op.KeyProof.Hash() {
		return nil, errInvalidOpCommand
	}

	var proofs []*wallets.SignedKeyExistenceProof
	err := json.Unmarshal(r.Data, &proofs)
	if err != nil {
		return nil, err
	}

	return proofs, nil
}

func (s *Service) update(action *types.ActionResult) (IDPair, bool, error) {
	if action.Status != 1 {
		return IDPair{}, false, errKeyUpdate
	}

	switch action.OPCommand {
	case op.KeyGenerate.Hash(), op.KeyDataProviderRestore.Hash(), op.KeyDirectRestore.Hash():
		id, err := s.updateOrAddKey(action)
		if err != nil {
			return IDPair{}, true, fmt.Errorf("updating or adding key: %w", err)
		}
		return id, true, nil

	case op.KeyDelete.Hash():
		id, err := s.removeKey(action)
		if err != nil {
			return IDPair{}, false, fmt.Errorf("removing key: %w", err)
		}
		return id, false, nil

	default:
		return IDPair{}, false, fmt.Errorf("unsupported action op command for key update %v", action.OPCommand)
	}
}

func (s *Service) updateOrAddKey(action *types.ActionResult) (IDPair, error) {
	keyInfo, err := parseNewKeyActionResult(action)
	if err != nil {
		return IDPair{}, fmt.Errorf("parsing new key action result: %w", err)
	}

	info, err := parseKeyExistenceProof(keyInfo)
	if err != nil {
		return IDPair{}, fmt.Errorf("parsing key existence proof: %w", err)
	}

	s.Lock()
	defer s.Unlock()

	keys, exists := s.KeysForWallet[info.WalletID]
	if !exists {
		keys = make([]uint64, 0, 1)
		s.KeysForWallet[info.WalletID] = keys
	}
	if !slices.Contains(keys, info.KeyID) {
		s.KeysForWallet[info.WalletID] = append(keys, info.KeyID)
	}

	id := IDPair{
		WalletID: info.WalletID,
		KeyID:    info.KeyID,
	}

	keyData, exists := s.Keys[id]
	if !exists {
		keyData = new(pkgwallets.KeyData)
		s.Keys[id] = keyData
	}
	keyData.Proof = keyInfo
	keyData.Info = *info

	return id, nil
}

func (s *Service) removeKey(action *types.ActionResult) (IDPair, error) {
	idPair, err := parseKeyDeleteActionResult(action)
	if err != nil {
		return IDPair{}, fmt.Errorf("parsing key delete action result: %w", err)
	}

	s.Lock()
	defer s.Unlock()

	delete(s.Keys, idPair)

	s.KeysForWallet[idPair.WalletID] = slices.DeleteFunc(s.KeysForWallet[idPair.WalletID], func(k uint64) bool {
		return k == idPair.KeyID
	})

	if len(s.KeysForWallet[idPair.WalletID]) == 0 {
		delete(s.KeysForWallet, idPair.WalletID)
	}

	return idPair, nil
}

// keysInfoAction prepares direct action with opType F_GET and opCommand KEY_INFO.
func keysInfoAction() (*types.Action, error) {
	return queue.PrepareDirectAction(op.Get, op.KeyInfo, nil)
}

// parseKeyDeleteActionResult parses action result for "KEY_DELETE" and returns the IDPair from result data.
func parseKeyDeleteActionResult(r *types.ActionResult) (IDPair, error) {
	if r.Status != 1 {
		return IDPair{}, errInvalidActionResult
	}

	if r.OPType != op.Wallet.Hash() {
		return IDPair{}, errInvalidOpType
	}

	if r.OPCommand != op.KeyDelete.Hash() {
		return IDPair{}, errInvalidOpCommand
	}

	var idPair IDPair
	err := json.Unmarshal(r.Data, &idPair)
	if err != nil {
		return IDPair{}, err
	}

	return idPair, nil
}

// parseNewKeyActionResult parses action result data for "KEY_GENERATE",
// "KEY_DATA_PROVIDER_RESTORE", or "KEY_DIRECT_RESTORE" — all three produce a
// SignedKeyExistenceProof so the proxy can hydrate its key cache the same way.
func parseNewKeyActionResult(r *types.ActionResult) (*wallets.SignedKeyExistenceProof, error) {
	if r.Status != 1 {
		return nil, errInvalidActionResult
	}

	if r.OPType != op.Wallet.Hash() {
		return nil, errInvalidOpType
	}

	if r.OPCommand != op.KeyDataProviderRestore.Hash() &&
		r.OPCommand != op.KeyGenerate.Hash() &&
		r.OPCommand != op.KeyDirectRestore.Hash() {
		return nil, errInvalidOpCommand
	}

	res := new(wallets.SignedKeyExistenceProof)
	err := json.Unmarshal(r.Data, res)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func parseKeyExistenceProof(proof *wallets.SignedKeyExistenceProof) (*pkgwallets.KeyExistence, error) {
	out := new(pkgwallets.KeyExistence)

	err := structs.DecodeTo(wallet.KeyExistenceStructArg, proof.KeyExistence, out)
	if err != nil {
		return nil, err
	}

	return out, nil
}

// PeriodicWalletsSyncTrigger adds a signal to the channel once per duration.
func PeriodicWalletsSyncTrigger(ctx context.Context, c chan bool, d time.Duration) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case c <- true:
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
