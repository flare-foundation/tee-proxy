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

	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
)

type IDPair = wallets.KeyIDPair

type Storage struct {
	KeysForWallet map[common.Hash][]uint64
	Keys          map[IDPair]*KeyData

	index   *storage.Storage[common.Hash]
	backups *storage.Storage[*wallets.TEEBackupResponse]

	aq *queue.ActionQueues
	rs *queue.ResponseStorage

	sync.RWMutex
}

func NewStorage(aq *queue.ActionQueues, rs *queue.ResponseStorage, client *redis.Client) Storage {
	kfw := make(map[common.Hash][]uint64)
	k := make(map[IDPair]*KeyData)

	bp := storage.New[*wallets.TEEBackupResponse]("backup", client)
	in := storage.New[common.Hash]("backupIndex", client)

	return Storage{
		KeysForWallet: kfw,
		Keys:          k,

		index:   in,
		backups: bp,

		aq: aq,
		rs: rs,
	}
}

func (s *Storage) RunInfo(ctx context.Context, trigger, backupTrigger <-chan bool, keyActions <-chan *types.ActionResult) {
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
			logger.Debug("walletID: %v keyID: %d %s", id.WalletID, id.KeyID, action)

			if added {
				go func() {
					logger.Debug("starting backup for %v", id)

					err := s.makeBackup(ctx, id)
					if err != nil {
						logger.Errorf("making backup for %v: %v", id, err)
					}
					logger.Debug("backup done for %v", id)
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

func (s *Storage) KeyProof(walletID common.Hash, keyID uint64) (*wallets.SignedKeyExistenceProof, error) {
	s.RLock()
	defer s.RUnlock()
	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, fmt.Errorf("%w: key proof not found", status.HTTP[404])
	}

	return info.Proof, nil
}

func (s *Storage) WalletInfo(walletID common.Hash) (*KeyExistence, error) {
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

// sync enqueues KEY_INFO action, waits for the response, and updated keys storage according to it.
//
// On each sync, maps s.Keys and s.KeysForWallet are build anew.
func (s *Storage) sync(ctx context.Context) error {
	action, err := keyInfoAction()
	if err != nil {
		return err
	}

	err = s.aq.Enqueue(ctx, action, processorutils.Direct)
	if err != nil {
		return err
	}

	response, err := s.rs.WaitOnResponse(ctx, action.Data.ID, action.Data.SubmissionTag, time.Minute)
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

func newKeys(r *types.ActionResult) (map[common.Hash][]uint64, map[IDPair]*KeyData, error) {
	proofs, err := parseKeyInfoActionResult(r)
	if err != nil {
		return nil, nil, err
	}

	keysForWallet := make(map[common.Hash][]uint64, len(proofs))
	keys := make(map[IDPair]*KeyData, len(proofs))

	for _, proof := range proofs {
		info, err := parse(proof)
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

		keyData := &KeyData{
			Info:  *info,
			Proof: proof,
		}
		keys[id] = keyData
	}

	return keysForWallet, keys, nil
}

func (s *Storage) update(action *types.ActionResult) (IDPair, bool, error) {
	if action.Status != 1 {
		return IDPair{}, false, errors.New("key update action not successful")
	}

	switch action.OPCommand {
	case op.KeyGenerate.Hash(), op.KeyDataProviderRestore.Hash():
		id, err := s.updateAdd(action)
		if err != nil {
			return IDPair{}, true, err
		}
		return id, true, err

	case op.KeyDelete.Hash():
		id, err := s.updateRemove(action)
		if err != nil {
			return IDPair{}, false, err
		}
		return id, false, err

	default:
		return IDPair{}, false, fmt.Errorf("unsupported action op command for key update %v", action.OPCommand)
	}
}

func (s *Storage) updateAdd(action *types.ActionResult) (IDPair, error) {
	keyInfo, err := parseNewKeyActionResult(action)
	if err != nil {
		return IDPair{}, err
	}

	info, err := parse(keyInfo)
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
		keyData = new(KeyData)
		s.Keys[id] = keyData
	}
	keyData.Proof = keyInfo
	keyData.Info = *info

	return id, nil
}

func (s *Storage) updateRemove(action *types.ActionResult) (IDPair, error) {
	idPair := IDPair{}
	err := json.Unmarshal(action.Data, &idPair)
	if err != nil {
		return idPair, err
	}

	delete(s.Keys, idPair)

	s.KeysForWallet[idPair.WalletID] = slices.DeleteFunc(s.KeysForWallet[idPair.WalletID], func(k uint64) bool {
		return k == idPair.KeyID
	})

	if len(s.KeysForWallet[idPair.WalletID]) == 0 {
		delete(s.KeysForWallet, idPair.WalletID)
	}

	return idPair, nil
}

// keyInfoAction prepares direct action with opType F_GET and opCommand KEY_INFO.
func keyInfoAction() (*types.Action, error) {
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

func parse(proof *wallets.SignedKeyExistenceProof) (*KeyExistence, error) {
	var out = new(KeyExistence)

	err := structs.DecodeTo(wallet.KeyExistenceStructArg, proof.KeyExistence, out)
	if err != nil {
		return nil, err
	}

	return out, nil
}
