package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/pkg/queue"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
)

type IDPair struct {
	WalletID common.Hash
	KeyID    uint64
}

type KeyData struct {
	Info  *wallet.ITeeWalletKeyManagerKeyExistence
	Proof *types.WalletSignedKeyExistenceProof
}

type Storage struct {
	KeysForWallet map[common.Hash][]uint64
	Keys          map[IDPair]*KeyData
	Backups       map[IDPair]*types.WalletGetBackupResponse // todo align this with docs

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

func (s *Storage) RunInfo(ctx context.Context, trigger chan bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			err := s.sync(ctx)
			if err != nil {
				continue
			}
		}
	}
}

func (s *Storage) sync(ctx context.Context) error {
	action, err := teeWalletAction()
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

	s.Lock()
	defer s.Unlock()
	err = s.storeWallets(&response.Result)
	if err != nil {
		return err
	}

	return nil
}

func (s *Storage) update(walletInfo *types.WalletSignedKeyExistenceProof) error {
	info, err := parse(walletInfo)
	if err != nil {
		return err
	}

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
	keyData.Proof = walletInfo
	keyData.Info = info

	return nil
}

func (s *Storage) storeWallets(result *types.ActionResult) error {
	// todo clear old storages

	data, err := parseActionResponse(result)
	if err != nil {
		return err
	}

	for j := range data {
		if err := s.update(data[j]); err != nil {
			return err
		}
	}

	return nil
}

func (s *Storage) KeyProof(walletID common.Hash, keyID uint64) (*types.WalletSignedKeyExistenceProof, error) {
	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, errors.New("wallet not found")
	}

	return info.Proof, nil
}

func (s *Storage) KeyInfo(walletID common.Hash, keyID uint64) (*wallet.ITeeWalletKeyManagerKeyExistence, error) {
	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, errors.New("wallet not found")
	}

	return info.Info, nil
}

func (s *Storage) WalletInfo(walletID common.Hash) (*wallet.ITeeWalletKeyManagerKeyExistence, error) {
	keys, exists := s.KeysForWallet[walletID]
	if !exists || len(keys) == 0 {
		return nil, errors.New("wallet not found")
	}

	return s.KeyInfo(walletID, keys[0])
}

func teeWalletAction() (*types.Action, error) {
	return queue.PrepareDirectAction(constants.Get, constants.KeyInfo, nil)
}

// todo: more checks needed?
// checks signatures
func parseActionResponse(r *types.ActionResult) ([]*types.WalletSignedKeyExistenceProof, error) {
	var res = make([]*types.WalletSignedKeyExistenceProof, 0)
	err := json.Unmarshal(r.Data, &res)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func parse(proof *types.WalletSignedKeyExistenceProof) (*wallet.ITeeWalletKeyManagerKeyExistence, error) {
	var out = new(wallet.ITeeWalletKeyManagerKeyExistence)

	err := structs.DecodeTo(wallet.KeyExistenceStructArg, proof.KeyExistenceProof, out)
	if err != nil {
		return nil, err
	}

	return out, nil
}
