package meta

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/fdc2"
	"github.com/flare-foundation/tee-node/pkg/fdc"
	"github.com/stretchr/testify/require"
)

func TestFDCMeta(t *testing.T) {
	m := New(nil, 0)

	atb := []byte("TeeAvailabilityCheck")
	at := common.Hash{}
	copy(at[:len(atb)], atb)

	srcb := []byte("TEE")
	src := common.Hash{}
	copy(src[:len(srcb)], srcb)

	cos1 := common.HexToAddress("c1")
	cos2 := common.HexToAddress("c2")

	ar := fdc2.IFdc2HubFdc2AttestationRequest{
		Header: fdc2.IFdc2HubFdc2RequestHeader{
			AttestationType: [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
			SourceId:        [32]byte{33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64},
			ThresholdBIPS:   7500, // 75%
		},
		RequestBody: []byte("todo"), // Sample request body
	}

	encoded, err := fdc.EncodeRequest(ar)
	require.NoError(t, err)

	ts := uint64(time.Now().Unix())

	data := instruction.DataFixed{
		InstructionID:          [32]byte{},
		TeeID:                  common.Address{},
		Timestamp:              ts,
		RewardEpochID:          0,
		OPType:                 op.FDC2.Hash(),
		OPCommand:              op.Prove.Hash(),
		OriginalMessage:        encoded,
		AdditionalFixedMessage: []byte("todo"),
		Cosigners:              []common.Address{cos1, cos2},
		CosignersThreshold:     2,
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
	hash, _, err := fdc.HashMessage(0, ar, []byte("todo"), data.Cosigners, data.CosignersThreshold, ts)
	require.NoError(t, err)

	sk, err := crypto.GenerateKey()
	require.NoError(t, err)

	sig, err := crypto.Sign(accounts.TextHash(hash[:]), sk)
	require.NoError(t, err)

	i := &instruction.Data{
		DataFixed:                 data,
		AdditionalVariableMessage: sig,
	}

	adr := crypto.PubkeyToAddress(sk.PublicKey)

	err = m.CheckConsistency(i, adr)
	require.NoError(t, err)

	err = m.CheckConsistency(i, common.Address{})
	require.Error(t, err)
}

func TestMetaGeneral(t *testing.T) {
	m := New(nil, 0)

	data := &instruction.DataFixed{
		InstructionID:          [32]byte{},
		TeeID:                  common.Address{},
		Timestamp:              0,
		RewardEpochID:          0,
		OPType:                 op.Wallet.Hash(),
		OPCommand:              op.KeyGenerate.Hash(),
		OriginalMessage:        []byte("todo"),
		AdditionalFixedMessage: []byte("todo"),
		Cosigners:              nil,
		CosignersThreshold:     0,
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
