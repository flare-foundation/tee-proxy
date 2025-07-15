package wallets

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
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

	as *queue.ActionQueues
	rs *queue.ResponseStorage

	sync.RWMutex
}

func (s *Storage) Sync(ctx context.Context) error {
	actionID := common.Hash{}
	_, err := io.ReadFull(rand.Reader, actionID[:])
	if err != nil {
		return err
	}

	action, err := teeWalletAction(actionID)
	if err != nil {
		return err
	}

	err = s.as.Enqueue(ctx, action, queue.Read)
	if err != nil {
		return err
	}

	result, err := s.waitOnResult(ctx, time.Minute, action)
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()
	err = s.storeWallets(result)
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

func (s *Storage) storeWallets(result *types.ActionResponse) error {
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

func teeWalletAction(id common.Hash) (*types.Action, error) {
	msg := types.DirectInstruction{
		Data: types.DirectInstructionData{
			OPType:    constants.Get.Hash(),
			OPCommand: constants.KeyInfo.Hash(),
			Message:   nil,
		},
		Signatures: nil,
	}

	m, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	ad := types.ActionData{
		ID:            id,
		Type:          types.Direct,
		SubmissionTag: types.Submit,
		Message:       m,
	}

	return &types.Action{
		Data:                       ad,
		Signatures:                 nil,
		AdditionalVariableMessages: nil,
		Timestamps:                 nil,
		AdditionalActionData:       []byte{},
	}, nil
}

// move this to result storage
func (is *Storage) waitOnResult(ctx context.Context, timeout time.Duration, action *types.Action) (*types.ActionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var result *types.ActionResponse
	var err error
	for {
		if err := ctx.Err(); err != nil {
			cancel()
			return nil, err
		}

		result, err = is.rs.GetResponse(ctx, action.Data.ID, action.Data.SubmissionTag) // todo retry
		if err == nil {
			break
		}

		time.Sleep(time.Second)
	}

	return result, err
}

// todo: more checks needed?
// checks signatures
func parseActionResponse(r *types.ActionResponse) ([]*types.WalletSignedKeyExistenceProof, error) {
	resB := r.Result.ResultData.Message

	var res = make([]*types.WalletSignedKeyExistenceProof, 0)

	err := json.Unmarshal(resB, &res)
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
