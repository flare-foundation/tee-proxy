package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flare-foundation/tee-node/pkg/op"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
)

type IDPair = types.WalletKeyIDPair

type KeyData struct {
	Info  *wallet.ITeeWalletKeyManagerKeyExistence `json:"info"`
	Proof *types.WalletSignedKeyExistenceProof     `json:"proof"`
}

type Storage struct {
	KeysForWallet map[common.Hash][]uint64
	Keys          map[IDPair]*KeyData

	Backups map[IDPair]*types.WalletGetBackupResponse // todo align this with docs

	aq *queue.ActionQueues
	rs *queue.ResponseStorage

	sync.RWMutex
}

func NewStorage(aq *queue.ActionQueues, rs *queue.ResponseStorage) Storage {
	kfw := make(map[common.Hash][]uint64)
	k := make(map[IDPair]*KeyData)
	b := make(map[IDPair]*types.WalletGetBackupResponse)

	return Storage{
		KeysForWallet: kfw,
		Keys:          k,
		Backups:       b,
		aq:            aq,
		rs:            rs,
	}
}

func (s *Storage) RunInfo(ctx context.Context, trigger <-chan bool, newKeys <-chan *types.ActionResult) {
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
		case newKeyAction := <-newKeys:
			logger.Debug("wallet key update start")
			err := s.update(newKeyAction)
			if err != nil {
				logger.Errorf("wallet key update: %w", err)
				continue
			}

			logger.Debug("wallet key update done")
		}
	}
}

func (s *Storage) KeyProof(walletID common.Hash, keyID uint64) (*types.WalletSignedKeyExistenceProof, error) {
	s.RLock()
	defer s.RUnlock()
	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, fmt.Errorf("%w: key proof not found", status.HTTP[404])
	}

	return info.Proof, nil
}

func (s *Storage) KeyData(walletID common.Hash, keyID uint64) (*KeyData, error) {
	s.RLock()
	defer s.RUnlock()

	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, fmt.Errorf("%w: key data not found", status.HTTP[404])
	}

	return info, nil
}

func (s *Storage) WalletInfo(walletID common.Hash) (*wallet.ITeeWalletKeyManagerKeyExistence, error) {
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

	return data.Info, nil
}

// sync enqueues KEY_INFO action, waits for the response, and updated keys storage according to it.
//
// On each sync, maps s.Keys and s.KeysForWallet are build anew.
func (s *Storage) sync(ctx context.Context) error {
	action, err := keyInfoAction()
	if err != nil {
		return err
	}

	err = s.aq.Enqueue(ctx, action, queue.Read)
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

		k, exists := keysForWallet[info.WalletId]
		if !exists {
			k = make([]uint64, 0)
			keysForWallet[info.WalletId] = k
		}
		keysForWallet[info.WalletId] = append(k, info.KeyId)

		id := IDPair{
			WalletID: info.WalletId,
			KeyID:    info.KeyId,
		}

		keyData := &KeyData{
			Info:  info,
			Proof: proof,
		}
		keys[id] = keyData
	}

	return keysForWallet, keys, nil
}

func (s *Storage) update(action *types.ActionResult) error {
	keyInfo, err := parseNewKeyActionResult(action)
	if err != nil {
		return err
	}

	info, err := parse(keyInfo)
	if err != nil {
		return err
	}
	s.Lock()
	defer s.Unlock()

	keys, exists := s.KeysForWallet[info.WalletId]
	if !exists {
		keys = make([]uint64, 0)
		s.KeysForWallet[info.WalletId] = keys
	}
	s.KeysForWallet[info.WalletId] = append(keys, info.KeyId)

	id := IDPair{
		WalletID: info.WalletId,
		KeyID:    info.KeyId,
	}

	keyData, exists := s.Keys[id]
	if !exists {
		keyData = new(KeyData)
		s.Keys[id] = keyData
	}
	keyData.Proof = keyInfo
	keyData.Info = info

	return nil
}

// keyInfoAction prepares direct action with opType F_GET and opCommand KEY_INFO.
func keyInfoAction() (*types.Action, error) {
	return queue.PrepareDirectAction(constants.Get, constants.KeyInfo, nil)
}

func parseKeyInfoActionResult(r *types.ActionResult) ([]*types.WalletSignedKeyExistenceProof, error) {
	if !r.Status {
		return nil, errors.New("invalid action result")
	}

	if r.OPType != op.Get.Hash() {
		return nil, errors.New("invalid action opType")
	}

	if r.OPCommand != op.KeyInfo.Hash() {
		return nil, errors.New("invalid action opCommand")
	}

	var res = make([]*types.WalletSignedKeyExistenceProof, 0)
	err := json.Unmarshal(r.Data, &res)
	if err != nil {
		return nil, err
	}

	return res, nil
}

// parseNewKeyActionResult parses action result data for "KEY_GENERATE" or "KEY_DATA_PROVIDER_RESTORE" action.
func parseNewKeyActionResult(r *types.ActionResult) (*types.WalletSignedKeyExistenceProof, error) {
	if !r.Status {
		return nil, errors.New("invalid action result status")
	}

	if r.OPType != op.Wallet.Hash() {
		return nil, fmt.Errorf("invalid action result opType, expected %v got %v", op.Wallet, op.HashToOPCommand(r.OPCommand))
	}

	if r.OPCommand != op.KeyDataProviderRestore.Hash() && r.OPCommand != op.KeyGenerate.Hash() {
		return nil, errors.New("invalid action result opCommand")
	}

	res := new(types.WalletSignedKeyExistenceProof)
	err := json.Unmarshal(r.Data, res)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func parse(proof *types.WalletSignedKeyExistenceProof) (*wallet.ITeeWalletKeyManagerKeyExistence, error) {
	var out = new(wallet.ITeeWalletKeyManagerKeyExistence)

	err := structs.DecodeTo(wallet.KeyExistenceStructArg, proof.KeyExistence, out)
	if err != nil {
		return nil, err
	}

	return out, nil
}
