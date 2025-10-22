package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/redis/go-redis/v9"

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

type IDPair = wallets.KeyIDPair

type Service struct {
	KeysForWallet map[common.Hash][]uint64
	Keys          map[IDPair]*pkgwallets.KeyData

	index   *storage.Storage[common.Hash]
	backups *storage.Storage[*wallets.TEEBackupResponse]

	aq *queue.ActionQueues
	rs *result.ResultStorage

	sync.RWMutex
}

func NewService(aq *queue.ActionQueues, rs *result.ResultStorage, client *redis.Client) Service {
	kfw := make(map[common.Hash][]uint64)
	k := make(map[IDPair]*pkgwallets.KeyData)

	bp := storage.New[*wallets.TEEBackupResponse]("backup", client)
	in := storage.New[common.Hash]("backupIndex", client)

	return Service{
		KeysForWallet: kfw,
		Keys:          k,

		index:   in,
		backups: bp,

		aq: aq,
		rs: rs,
	}
}

func (s *Service) RunUpdateInfo(ctx context.Context, trigger, backupTrigger <-chan bool, keyActions <-chan *types.ActionResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			logger.Debug("wallet sync start")

			err := s.sync(ctx)
			if err != nil {
				logger.Errorf("wallet sync: %v", err)
				continue
			}

			logger.Debug("wallet sync done")
		case keyAction := <-keyActions:
			logger.Debug("wallet key update start")
			id, added, err := s.update(keyAction)
			if err != nil {
				logger.Errorf("wallet key update: %w", err)
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

					err := s.makeBackup(ctx, id)
					if err != nil {
						logger.Errorf("making backup for %v: %v", id, err)
					}
					logger.Debugf("backup done for %v", id)
				}()
			}

		case <-backupTrigger:
			logger.Debug("backup triggered")
			err := s.MakeBackups(ctx)
			if err != nil {
				logger.Errorf("error creating backup. First error: %w", err)
				continue
			}
			logger.Debug("backups done")
		}
	}
}

func (s *Service) KeyProof(walletID common.Hash, keyID uint64) (*wallets.SignedKeyExistenceProof, error) {
	s.RLock()
	defer s.RUnlock()
	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, fmt.Errorf("%w: key proof not found", status.HTTP[404])
	}

	return info.Proof, nil
}

func (s *Service) WalletInfo(walletID common.Hash) (*pkgwallets.KeyExistence, error) {
	s.RLock()
	defer s.RUnlock()

	keys, exists := s.KeysForWallet[walletID]
	if !exists || len(keys) == 0 {
		return nil, errors.New("wallet not found")
	}

	data, err := s.KeyData(walletID, keys[0])
	if err != nil {
		return nil, err
	}

	return &data.Info, nil
}

func (s *Service) KeyData(walletID common.Hash, keyID uint64) (*pkgwallets.KeyData, error) {
	s.RLock()
	defer s.RUnlock()

	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, fmt.Errorf("%w: key data not found", status.HTTP[404])
	}

	return info, nil
}

// sync enqueues KEY_INFO action, waits for the response, and updated keys storage according to it.
//
// On each sync, maps s.Keys and s.KeysForWallet are build anew.
func (s *Service) sync(ctx context.Context) error {
	action, err := keysInfoAction()
	if err != nil {
		return err
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return err
	}

	response, err := s.rs.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, time.Minute) // todo: constant
	if err != nil {
		return err
	}

	newKeysForWallet, newKeys, err := newKeys(&response.Result)
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()
	s.Keys = newKeys
	s.KeysForWallet = newKeysForWallet

	return nil
}

func newKeys(r *types.ActionResult) (map[common.Hash][]uint64, map[IDPair]*pkgwallets.KeyData, error) {
	proofs, err := parseKeyInfoActionResult(r)
	if err != nil {
		return nil, nil, err
	}

	keysForWallet := make(map[common.Hash][]uint64, len(proofs))
	keys := make(map[IDPair]*pkgwallets.KeyData, len(proofs))

	for _, proof := range proofs {
		info, err := parseKeyExistenceProof(proof)
		if err != nil {
			return nil, nil, err
		}

		k, exists := keysForWallet[info.WalletID]
		if !exists {
			k = make([]uint64, 0)
			keysForWallet[info.WalletID] = k
		}
		keysForWallet[info.WalletID] = append(k, info.KeyID)

		id := IDPair{
			WalletID: info.WalletID,
			KeyID:    info.KeyID,
		}

		keyData := &pkgwallets.KeyData{
			Info:  *info,
			Proof: proof,
		}
		keys[id] = keyData
	}

	return keysForWallet, keys, nil
}

func (s *Service) update(action *types.ActionResult) (IDPair, bool, error) {
	if action.Status != 1 {
		return IDPair{}, false, errors.New("key update action not successful")
	}

	switch action.OPCommand {
	case op.KeyGenerate.Hash(), op.KeyDataProviderRestore.Hash():
		id, err := s.updateOrAddKey(action)
		if err != nil {
			return IDPair{}, true, err
		}
		return id, true, err

	case op.KeyDelete.Hash():
		id, err := s.removeKey(action)
		if err != nil {
			return IDPair{}, false, err
		}
		return id, false, err

	default:
		return IDPair{}, false, fmt.Errorf("unsupported action op command for key update %v", action.OPCommand)
	}
}

func (s *Service) updateOrAddKey(action *types.ActionResult) (IDPair, error) {
	keyInfo, err := parseNewKeyActionResult(action)
	if err != nil {
		return IDPair{}, err
	}

	info, err := parseKeyExistenceProof(keyInfo)
	if err != nil {
		return IDPair{}, err
	}

	s.Lock()
	defer s.Unlock()

	keys, exists := s.KeysForWallet[info.WalletID]
	if !exists {
		keys = make([]uint64, 0)
		s.KeysForWallet[info.WalletID] = keys
	}
	s.KeysForWallet[info.WalletID] = append(keys, info.KeyID)

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
		return IDPair{}, err
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

func parseKeyInfoActionResult(r *types.ActionResult) ([]*wallets.SignedKeyExistenceProof, error) {
	if r.Status != 1 {
		return nil, errors.New("invalid action result")
	}

	if r.OPType != op.Get.Hash() {
		return nil, errors.New("invalid action opType")
	}

	if r.OPCommand != op.KeyInfo.Hash() {
		return nil, errors.New("invalid action opCommand")
	}

	var res = make([]*wallets.SignedKeyExistenceProof, 0)
	err := json.Unmarshal(r.Data, &res)
	if err != nil {
		return nil, err
	}

	return res, nil
}

// parseKeyDeleteActionResult parses action result for "KEY_DELETE" and returns the IDPair from result data.
func parseKeyDeleteActionResult(r *types.ActionResult) (IDPair, error) {
	if r.Status != 1 {
		return IDPair{}, errors.New("invalid action result status")
	}

	if r.OPType != op.Wallet.Hash() {
		return IDPair{}, errors.New("invalid action result opType")
	}

	if r.OPCommand != op.KeyDelete.Hash() {
		return IDPair{}, errors.New("invalid action result opCommand")
	}

	var idPair IDPair
	err := json.Unmarshal(r.Data, &idPair)
	if err != nil {
		return IDPair{}, err
	}

	return idPair, nil
}

// parseNewKeyActionResult parses action result data for "KEY_GENERATE" or "KEY_DATA_PROVIDER_RESTORE" action.
func parseNewKeyActionResult(r *types.ActionResult) (*wallets.SignedKeyExistenceProof, error) {
	if r.Status != 1 {
		return nil, errors.New("invalid action result status")
	}

	if r.OPType != op.Wallet.Hash() {
		return nil, fmt.Errorf("invalid action result opType, expected %v got %v", op.Wallet, op.HashToOPCommand(r.OPCommand))
	}

	if r.OPCommand != op.KeyDataProviderRestore.Hash() && r.OPCommand != op.KeyGenerate.Hash() {
		return nil, errors.New("invalid action result opCommand")
	}

	res := new(wallets.SignedKeyExistenceProof)
	err := json.Unmarshal(r.Data, res)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func parseKeyExistenceProof(proof *wallets.SignedKeyExistenceProof) (*pkgwallets.KeyExistence, error) {
	var out = new(pkgwallets.KeyExistence)

	err := structs.DecodeTo(wallet.KeyExistenceStructArg, proof.KeyExistence, out)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func PeriodicWalletsSyncTrigger(ctx context.Context, c chan bool, d time.Duration) {
	for {
		if ctx.Err() != nil {
			return
		}

		c <- true

		time.Sleep(d)
	}
}
