package wallets

import (
	"encoding/json"
	"testing"
	"time"

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

	kep := wal.KeyExistenceProof(teeID)

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
