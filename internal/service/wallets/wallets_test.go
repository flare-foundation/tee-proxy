package wallets

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

func TestNewKey(t *testing.T) {
	adminKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	teeKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	teeID := crypto.PubkeyToAddress(teeKey.PublicKey)

	walletID := common.BytesToHash([]byte("walletID"))

	actionMsg := &wallet.IWalletKeyManagerKeyGenerate{
		SigningAlgo: wallets.XRPSignAlgo,
		KeyType:     wallets.XRPType,
		TeeId:       teeID,
		WalletId:    walletID,
		KeyId:       0,
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

	wal, err := wallets.GenerateNewKey(*actionMsg)
	require.NoError(t, err)

	kep, err := wal.KeyExistenceProof(teeID)
	require.NoError(t, err)

	existenceProofEncoded, err := structs.Encode(wallet.KeyExistenceStructArg, kep)
	require.NoError(t, err)

	hash := crypto.Keccak256(existenceProofEncoded)
	signature, err := crypto.Sign(accounts.TextHash(hash), teeKey)
	require.NoError(t, err)

	signedProof := wallets.SignedKeyExistenceProof{
		KeyExistence: existenceProofEncoded,
		Signature:    signature,
	}

	resultEncoded, err := json.Marshal(signedProof)
	require.NoError(t, err)

	aResult := &types.ActionResult{
		ID:                     common.Hash{},
		SubmissionTag:          types.Threshold,
		Status:                 1,
		Log:                    "",
		OPType:                 op.Wallet.Hash(),
		OPCommand:              op.KeyGenerate.Hash(),
		AdditionalResultStatus: hexutil.Bytes{},
		Version:                "",
		Data:                   resultEncoded,
	}

	str := NewService(nil, nil, nil, nil, time.Hour, nil)

	idPair, added, err := str.update(aResult)
	require.NoError(t, err)

	require.True(t, added)
	require.Equal(t, walletID, idPair.WalletID)
	require.Equal(t, uint64(0), idPair.KeyID)

	sResult, err := str.WalletInfo(walletID)
	require.NoError(t, err)

	require.Equal(t, teeID, sResult.TeeID)
	require.Equal(t, walletID, sResult.WalletID)
	require.Equal(t, wallets.XRPSignAlgo, sResult.SigningAlgo)
	require.Equal(t, wallets.XRPType, sResult.KeyType)

	sProof, err := str.KeyProof(walletID, 0)
	require.NoError(t, err)

	require.Equal(t, hexutil.Bytes(existenceProofEncoded), sProof.KeyExistence)

	// key does not exist
	_, err = str.KeyProof(walletID, 1)
	require.Error(t, err)

	_, err = str.KeyData(walletID, 1)
	require.Error(t, err)

	// wallet does not exist
	_, err = str.WalletInfo(common.BytesToHash([]byte("nonexistent")))
	require.Error(t, err)

	keyIDEncoded, err := json.Marshal(idPair)
	require.NoError(t, err)

	aResultDelete := &types.ActionResult{
		ID:                     common.Hash{},
		SubmissionTag:          types.Threshold,
		Status:                 1,
		Log:                    "",
		OPType:                 op.Wallet.Hash(),
		OPCommand:              op.KeyDelete.Hash(),
		AdditionalResultStatus: hexutil.Bytes{},
		Version:                "",
		Data:                   keyIDEncoded,
	}

	idPairDelete, added, err := str.update(aResultDelete)
	require.NoError(t, err)
	require.False(t, added)
	require.Equal(t, idPair, idPairDelete)

	_, err = str.WalletInfo(walletID)
	require.Error(t, err)
}

// nodeWaitCount reads teeproxy_node_response_wait_total for the given path/result label
// pair from m's registry, returning 0 if no matching series exists.
func nodeWaitCount(t *testing.T, m *metrics.Metrics, path, result string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() != "teeproxy_node_response_wait_total" {
			continue
		}
		for _, mc := range f.GetMetric() {
			var gotPath, gotResult string
			for _, l := range mc.GetLabel() {
				switch l.GetName() {
				case "path":
					gotPath = l.GetValue()
				case "result":
					gotResult = l.GetValue()
				}
			}
			if gotPath == path && gotResult == result {
				return mc.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// TestSyncNodeWaitLabels pins the wallet_key_info/wallet_key_proof node-wait path labels
// end to end through sync(), complementing wallets_sync_test.go's wallet_sync_total coverage.
func TestSyncNodeWaitLabels(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		mr := miniredis.RunT(t)
		c := storage.NewClient(mr.Addr())
		n := storage.NewNotifier(c)
		rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
		aq := queue.NewActionQueues(c, time.Hour, nil)

		m := metrics.New(metrics.Config{Enable: true, Node: true})
		svc := NewService(aq, rs, nil, nil, time.Hour, m)

		k0Wallet := common.BytesToHash([]byte("wallet-nodewait-ok"))
		k0Proof := makeSignedProof(t, k0Wallet, 0)
		remoteInfo := []types.KeyInfo{{WalletID: k0Wallet, KeyID: 0, Nonce: 0}}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		go func() {
			for {
				action, err := dequeueDirect(ctx, aq)
				if err != nil {
					return
				}

				var di types.DirectInstruction
				if err := json.Unmarshal(action.Data.Message, &di); err != nil {
					return
				}

				switch di.OPCommand {
				case op.KeyInfo.Hash():
					data, err := json.Marshal(remoteInfo)
					if err != nil {
						return
					}
					if err := storeGetResponse(ctx, rs, action, op.KeyInfo, data); err != nil {
						return
					}
				case op.KeyProof.Hash():
					data, err := json.Marshal([]*wallets.SignedKeyExistenceProof{k0Proof})
					if err != nil {
						return
					}
					_ = storeGetResponse(ctx, rs, action, op.KeyProof, data)
					return
				default:
					return
				}
			}
		}()

		require.NoError(t, svc.sync(ctx))

		require.Equal(t, float64(1), nodeWaitCount(t, m, "wallet_key_info", "ok"))
		require.Equal(t, float64(1), nodeWaitCount(t, m, "wallet_key_proof", "ok"))
	})

	// The KEY_INFO action is never answered, so fetchKeyInfo's WaitOnResponse call runs out
	// the sync ctx's deadline.
	t.Run("key_info_timeout", func(t *testing.T) {
		mr := miniredis.RunT(t)
		c := storage.NewClient(mr.Addr())
		n := storage.NewNotifier(c)
		rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), n, time.Hour, time.Hour)
		aq := queue.NewActionQueues(c, time.Hour, nil)

		m := metrics.New(metrics.Config{Enable: true, Node: true})
		svc := NewService(aq, rs, nil, nil, time.Hour, m)

		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()

		require.Error(t, svc.sync(ctx))

		// A WaitOnResponse expiry surfaces as a net read-deadline error, which
		// nodeWaitResult classifies as "timeout".
		require.Equal(t, float64(1), nodeWaitCount(t, m, "wallet_key_info", "timeout"))
		require.Equal(t, float64(0), nodeWaitCount(t, m, "wallet_key_info", "error"))
	})
}

// counterValue reads the single-series counter name from m's registry, returning 0 if the
// family is absent.
func counterValue(t *testing.T, m *metrics.Metrics, name string) float64 {
	t.Helper()

	fams, err := m.Registry().Gather()
	require.NoError(t, err)

	for _, f := range fams {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}

	return 0
}

// TestCreateNewBackupDecodeFailureCounts guards the pre-storage failure accounting: an
// undecodable or nil TEE_BACKUP result increments wallet_backup_apply_failed_total, so
// backup-pipeline breakage (node bug or version skew) is visible to alerting.
func TestCreateNewBackupDecodeFailureCounts(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, Wallet: true})
	svc := NewService(nil, nil, nil, nil, time.Hour, m)

	err := svc.createNewBackup(context.Background(), &types.ActionResult{Data: []byte("not json")})
	require.Error(t, err)

	err = svc.createNewBackup(context.Background(), &types.ActionResult{Data: []byte("null")})
	require.Error(t, err)

	require.Equal(t, float64(2), counterValue(t, m, "teeproxy_wallet_backup_apply_failed_total"))
}
