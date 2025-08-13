package meta

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/connector"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/payment"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/wallet"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
	"github.com/stretchr/testify/require"
)

func TestFTDCMeta(t *testing.T) {
	m := New(nil)

	atb := []byte("TeeAvailabilityCheck")
	at := common.Hash{}
	copy(at[:len(atb)], atb)

	srcb := []byte("TEE")
	src := common.Hash{}
	copy(src[:len(srcb)], srcb)

	cos1 := common.HexToAddress("c1")
	cos2 := common.HexToAddress("c2")

	ar := connector.IFtdcHubFtdcAttestationRequest{
		Header: connector.IFtdcHubFtdcRequestHeader{
			AttestationType:    [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
			SourceId:           [32]byte{33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64},
			ThresholdBIPS:      7500, // 75%
			Cosigners:          []common.Address{cos1, cos2},
			CosignersThreshold: 2,
		},
		RequestBody: []byte("todo"), // Sample request body
	}

	encoded, err := types.EncodeFTDCRequest(ar)
	require.NoError(t, err)

	ts := uint64(time.Now().Unix())

	data := instruction.DataFixed{
		InstructionID:          [32]byte{},
		TeeID:                  common.Address{},
		Timestamp:              ts,
		RewardEpochID:          0,
		OPType:                 op.FTDC.Hash(),
		OPCommand:              op.Prove.Hash(),
		OriginalMessage:        encoded,
		AdditionalFixedMessage: []byte("todo"),
	}

	// threshold
	thrsh, err := m.ThresholdBIPS(&data)
	require.NoError(t, err)

	require.Equal(t, 7500, thrsh)

	// cosigners
	cs, cst, err := m.Cosigners(&data)
	require.NoError(t, err)

	require.True(t, cs[cos1])
	require.True(t, cs[cos2])
	require.Len(t, cs, 2)
	require.Equal(t, uint64(2), cst)

	// consistency
	hash, _, err := types.HashFTDCMessage(ar, []byte("todo"), ts)
	require.NoError(t, err)

	pk, err := crypto.GenerateKey()
	require.NoError(t, err)

	sig, err := crypto.Sign(accounts.TextHash(hash[:]), pk)
	require.NoError(t, err)

	i := &instruction.Data{
		DataFixed:                 data,
		AdditionalVariableMessage: sig,
	}

	adr := crypto.PubkeyToAddress(pk.PublicKey)

	err = m.CheckConsistency(i, adr)
	require.NoError(t, err)

	err = m.CheckConsistency(i, common.Address{})
	require.Error(t, err)
}

func TestMetaGeneral(t *testing.T) {
	m := New(nil)

	data := &instruction.DataFixed{
		InstructionID:          [32]byte{},
		TeeID:                  common.Address{},
		Timestamp:              0,
		RewardEpochID:          0,
		OPType:                 op.Wallet.Hash(),
		OPCommand:              op.KeyGenerate.Hash(),
		OriginalMessage:        []byte("todo"),
		AdditionalFixedMessage: []byte("todo"),
	}

	thrsh, err := m.ThresholdBIPS(data)
	require.NoError(t, err)

	require.Equal(t, -1, thrsh)

	cs, cst, err := m.Cosigners(data)
	require.NoError(t, err)

	require.Len(t, cs, 0)
	require.Equal(t, uint64(0), cst)

	anyAddress := common.BytesToAddress([]byte("anyAddress"))

	err = m.CheckConsistency(
		&instruction.Data{
			DataFixed:                 *data,
			AdditionalVariableMessage: hexutil.Bytes{},
		}, anyAddress)
	require.NoError(t, err)
}

func TestXRPCosigners(t *testing.T) {
	ws := wallets.NewStorage(nil, nil)

	wID := common.BytesToHash([]byte("walletID"))

	kID := uint64(2)

	cosigner := common.BytesToAddress([]byte("cosigner"))

	teeID := common.BytesToAddress([]byte("teeID"))

	ke := &wallet.ITeeWalletKeyManagerKeyExistence{
		TeeId:             teeID,
		WalletId:          wID,
		KeyId:             kID,
		OpType:            op.XRP.Hash(),
		PublicKey:         []byte{},
		ProofOfPossession: []byte{},
		Nonce:             &big.Int{},
		PauseNonce:        &big.Int{},
		Status:            0,
		Restored:          false,
		AddressStr:        "",
		ConfigConstants: wallet.ITeeWalletKeyManagerKeyConfigConstants{
			AdminsPublicKeys:   []wallet.PublicKey{},
			AdminsThreshold:    0,
			Cosigners:          []common.Address{cosigner},
			CosignersThreshold: 1,
			OpTypeConstants:    []byte{},
		},
		ConfigSettings: wallet.ITeeWalletKeyManagerKeyConfigSettings{},
	}

	idPair := types.WalletKeyIDPair{
		WalletID: wID,
		KeyID:    kID,
	}

	ws.Keys[idPair] = &wallets.KeyData{
		Info:  ke,
		Proof: &types.WalletSignedKeyExistenceProof{},
	}

	ws.KeysForWallet[wID] = []uint64{kID}

	m := New(&ws)

	om := payment.ITeePaymentsPaymentInstructionMessage{
		WalletId: wID,
		TeeIdKeyIdPairs: []payment.TeeIdKeyIdPair{{
			TeeId: teeID,
			KeyId: kID,
		}},
		SenderAddress:    "rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G",
		RecipientAddress: "rogue5HnPRSszD9CWGSUz8UGHMVwSSKF6",
		Amount:           big.NewInt(100),
		Fee:              big.NewInt(20),
		PaymentReference: [32]byte{},
		Nonce:            0,
		SubNonce:         0,
		BatchEndTs:       0,
	}

	encoded, err := abi.Arguments{payment.MessageArguments[op.Pay]}.Pack(om)
	require.NoError(t, err)

	data := instruction.DataFixed{
		InstructionID:          [32]byte{},
		TeeID:                  teeID,
		Timestamp:              0,
		RewardEpochID:          0,
		OPType:                 op.XRP.Hash(),
		OPCommand:              op.Pay.Hash(),
		OriginalMessage:        encoded,
		AdditionalFixedMessage: nil,
	}

	cs, cst, err := m.Cosigners(&data)
	require.NoError(t, err)

	require.True(t, cs[cosigner])
	require.Len(t, cs, 1)

	require.Equal(t, uint64(1), cst)

	// no wallet

	otherWallet := common.BytesToHash([]byte("otherWallet"))

	om = payment.ITeePaymentsPaymentInstructionMessage{
		WalletId: otherWallet,
		TeeIdKeyIdPairs: []payment.TeeIdKeyIdPair{{
			TeeId: teeID,
			KeyId: kID,
		}},
		SenderAddress:    "rN5N6fJbc8xyViPDeQFMQMpYfVHuxSGV2G",
		RecipientAddress: "rogue5HnPRSszD9CWGSUz8UGHMVwSSKF6",
		Amount:           big.NewInt(100),
		Fee:              big.NewInt(20),
		PaymentReference: [32]byte{},
		Nonce:            0,
		SubNonce:         0,
		BatchEndTs:       0,
	}

	encoded, err = abi.Arguments{payment.MessageArguments[op.Pay]}.Pack(om)
	require.NoError(t, err)

	data = instruction.DataFixed{
		InstructionID:          [32]byte{},
		TeeID:                  teeID,
		Timestamp:              0,
		RewardEpochID:          0,
		OPType:                 op.XRP.Hash(),
		OPCommand:              op.Pay.Hash(),
		OriginalMessage:        encoded,
		AdditionalFixedMessage: nil,
	}

	_, _, err = m.Cosigners(&data)
	require.Error(t, err)
}
