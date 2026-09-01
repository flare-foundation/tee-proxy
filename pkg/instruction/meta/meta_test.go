package meta

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/fdc2"
	vrfstruct "github.com/flare-foundation/go-flare-common/pkg/tee/structs/vrf"
	"github.com/flare-foundation/tee-node/pkg/fdc"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/wallets/backup"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	pkgwallets "github.com/flare-foundation/tee-proxy/pkg/wallets"
)

func TestFDCMeta(t *testing.T) {
	m := New(nil, 14)

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

	// consistency: data providers / cosigners sign the chain-bound Relay Mode-2
	// prefixed hash, not messageHash directly (see fdc.ChainBoundRelayPrefixedHash).
	hash, _, err := fdc.HashMessage(uint64(14), ar, []byte("todo"), data.Cosigners, data.CosignersThreshold, ts)
	require.NoError(t, err)
	dpSigningHash := fdc.ChainBoundRelayPrefixedHash(uint64(14), hash)

	sk, err := crypto.GenerateKey()
	require.NoError(t, err)

	sig, err := crypto.Sign(accounts.TextHash(dpSigningHash[:]), sk)
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
	m := New(nil, 14)

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

func encodeFDC2Request(t *testing.T, thresholdBIPS uint16) []byte {
	t.Helper()

	req := fdc2.IFdc2HubFdc2AttestationRequest{
		Header: fdc2.IFdc2HubFdc2RequestHeader{
			AttestationType: [32]byte{1},
			SourceId:        [32]byte{2},
			ThresholdBIPS:   thresholdBIPS,
		},
		RequestBody: []byte("body"),
	}

	encoded, err := fdc.EncodeRequest(req)
	require.NoError(t, err)

	return encoded
}

func TestThresholdBIPSFDC2(t *testing.T) {
	m := New(nil, 14)

	a := common.HexToAddress("a1")
	b := common.HexToAddress("a2")
	c := common.HexToAddress("a3")

	tests := []struct {
		name        string
		bips        uint16
		cosigners   []common.Address
		coThreshold uint64
		want        int
		wantErr     error
	}{
		{name: "zero falls back to policy default", bips: 0, want: -1},
		{name: "below minimum", bips: 3999, cosigners: []common.Address{a, b, c}, coThreshold: 2, wantErr: ErrFDCThresholdTooLow},
		{name: "minimum with cosigner majority", bips: 4000, cosigners: []common.Address{a, b, c}, coThreshold: 2, want: 4000},
		{name: "below half without cosigner majority", bips: 4500, cosigners: []common.Address{a, b}, coThreshold: 1, wantErr: ErrFDCThresholdBelowHalf},
		{name: "below half with cosigner majority", bips: 4500, cosigners: []common.Address{a, b}, coThreshold: 2, want: 4500},
		{name: "at half", bips: 5000, want: 5000},
		{name: "high accepted", bips: 9999, want: 9999},
		{name: "at maximum rejected", bips: 10000, cosigners: []common.Address{a, b, c}, coThreshold: 2, wantErr: ErrFDCThresholdTooHigh},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := &instruction.DataFixed{
				OPType:             op.FDC2.Hash(),
				OPCommand:          op.Prove.Hash(),
				OriginalMessage:    encodeFDC2Request(t, tt.bips),
				Cosigners:          tt.cosigners,
				CosignersThreshold: tt.coThreshold,
			}

			got, err := m.ThresholdBIPS(data)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func encodeVRFMessage(t *testing.T, walletID common.Hash, keyID uint64) []byte {
	t.Helper()

	enc, err := structs.Encode(vrfstruct.MessageArguments[op.VRF], vrfstruct.IVrfVrfInstructionMessage{
		WalletId: walletID,
		KeyId:    keyID,
		Nonce:    []byte("nonce"),
	})
	require.NoError(t, err)

	return enc
}

func walletService(t *testing.T, walletID common.Hash, keyID uint64, cosigners []common.Address, threshold uint64) *wallets.Service {
	t.Helper()

	return &wallets.Service{
		KeysForWallet: map[common.Hash][]uint64{walletID: {keyID}},
		Keys: map[wallets.IDPair]*pkgwallets.KeyData{
			{WalletID: walletID, KeyID: keyID}: {
				Info: pkgwallets.KeyExistence{
					WalletID: walletID,
					KeyID:    keyID,
					ConfigConstants: pkgwallets.ConfigConstants{
						Cosigners:          cosigners,
						CosignersThreshold: threshold,
					},
				},
			},
		},
	}
}

func TestCosignersRejectDuplicates(t *testing.T) {
	a := common.HexToAddress("a1")
	b := common.HexToAddress("a2")

	tests := []struct {
		name    string
		command op.Command
	}{
		{name: "client declared set", command: op.KeyGenerate},
		{name: "xrp payment", command: op.Pay},
		{name: "vrf", command: op.VRF},
		{name: "data provider restore", command: op.KeyDataProviderRestore},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nil wallets service: the guard must fire before any roster lookup.
			m := New(nil, 14)

			_, _, err := m.Cosigners(&instruction.DataFixed{
				OPType:             op.Wallet.Hash(),
				OPCommand:          tt.command.Hash(),
				Cosigners:          []common.Address{a, b, a},
				CosignersThreshold: 2,
			})
			require.ErrorIs(t, err, ErrDuplicateCosigners)
		})
	}
}

func TestVRFCosigners(t *testing.T) {
	const keyID = 7

	walletID := common.HexToHash("abc")

	a := common.HexToAddress("a1")
	b := common.HexToAddress("a2")
	c := common.HexToAddress("a3")

	tests := []struct {
		name      string
		declared  []common.Address
		threshold uint64
		walletID  common.Hash
		wantErr   error
	}{
		{name: "matching roster", declared: []common.Address{a, b}, threshold: 2, walletID: walletID},
		{name: "order does not matter", declared: []common.Address{b, a}, threshold: 2, walletID: walletID},
		{name: "extra cosigner", declared: []common.Address{a, b, c}, threshold: 2, walletID: walletID, wantErr: ErrCosignerMismatch},
		{name: "missing cosigner", declared: []common.Address{a}, threshold: 2, walletID: walletID, wantErr: ErrCosignerMismatch},
		{name: "duplicate padded to roster length", declared: []common.Address{a, a}, threshold: 2, walletID: walletID, wantErr: ErrDuplicateCosigners},
		{name: "wrong threshold", declared: []common.Address{a, b}, threshold: 1, walletID: walletID, wantErr: ErrCosignerThresholdMismatch},
		{name: "unknown wallet", declared: []common.Address{a, b}, threshold: 2, walletID: common.HexToHash("dead"), wantErr: wallets.ErrWalletNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(walletService(t, walletID, keyID, []common.Address{a, b}, 2), 14)

			cs, threshold, err := m.Cosigners(&instruction.DataFixed{
				OPType:             op.Wallet.Hash(),
				OPCommand:          op.VRF.Hash(),
				OriginalMessage:    encodeVRFMessage(t, tt.walletID, keyID),
				Cosigners:          tt.declared,
				CosignersThreshold: tt.threshold,
			})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, map[common.Address]bool{a: true, b: true}, cs)
			require.Equal(t, uint64(2), threshold)
		})
	}

	t.Run("malformed message", func(t *testing.T) {
		m := New(walletService(t, walletID, keyID, []common.Address{a, b}, 2), 14)

		_, _, err := m.Cosigners(&instruction.DataFixed{
			OPType:             op.Wallet.Hash(),
			OPCommand:          op.VRF.Hash(),
			OriginalMessage:    []byte("not abi encoded"),
			Cosigners:          []common.Address{a, b},
			CosignersThreshold: 2,
		})
		require.ErrorIs(t, err, ErrMalformedPayload)
	})
}

func adminKey(t *testing.T) (types.PublicKey, common.Address) {
	t.Helper()

	sk, err := crypto.GenerateKey()
	require.NoError(t, err)

	return types.PubKeyToStruct(&sk.PublicKey), crypto.PubkeyToAddress(sk.PublicKey)
}

func restoreData(t *testing.T, md backup.WalletBackupMetaData, declared []common.Address, threshold uint64) *instruction.DataFixed {
	t.Helper()

	enc, err := json.Marshal(md)
	require.NoError(t, err)

	return &instruction.DataFixed{
		OPType:                 op.Wallet.Hash(),
		OPCommand:              op.KeyDataProviderRestore.Hash(),
		AdditionalFixedMessage: enc,
		Cosigners:              declared,
		CosignersThreshold:     threshold,
	}
}

// TestRestoreBackupMetadata pins the backup-metadata rules the node enforces in
// keyRestoreDataCheck. The metadata is unauthenticated beyond its backup ID, which does not
// cover the rosters, so a declared cosigner list can be clean while the metadata is not.
func TestRestoreBackupMetadata(t *testing.T) {
	m := New(nil, 14)

	pubA, addrA := adminKey(t)
	pubB, addrB := adminKey(t)

	c1 := common.HexToAddress("c1")
	c2 := common.HexToAddress("c2")

	manyAdmins := make([]types.PublicKey, 51)
	for i := range manyAdmins {
		manyAdmins[i] = pubA
	}

	manyCosigners := make([]common.Address, 51)
	for i := range manyCosigners {
		manyCosigners[i] = common.BytesToAddress([]byte{byte(i + 1)})
	}

	tests := []struct {
		name              string
		admins            []types.PublicKey
		adminsThreshold   uint64
		cosigners         []common.Address
		cosignerThreshold uint64
		declared          []common.Address
		declaredThreshold uint64
		wantErr           error
	}{
		{
			name: "valid metadata", admins: []types.PublicKey{pubA, pubB}, adminsThreshold: 2,
			cosigners: []common.Address{c1, c2}, cosignerThreshold: 2,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 2,
		},
		{
			name: "duplicate admins", admins: []types.PublicKey{pubA, pubA, pubB}, adminsThreshold: 2,
			cosigners: []common.Address{c1, c2}, cosignerThreshold: 2,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 2,
			wantErr: ErrInvalidBackupMetadata,
		},
		{
			name: "duplicate metadata cosigners", admins: []types.PublicKey{pubA, pubB}, adminsThreshold: 2,
			cosigners: []common.Address{c1, c1}, cosignerThreshold: 1,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 2,
			wantErr: ErrInvalidBackupMetadata,
		},
		{
			name: "cosigner threshold exceeds set", admins: []types.PublicKey{pubA, pubB}, adminsThreshold: 2,
			cosigners: []common.Address{c1, c2}, cosignerThreshold: 3,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 2,
			wantErr: ErrInvalidBackupMetadata,
		},
		{
			name: "too many admins", admins: manyAdmins, adminsThreshold: 2,
			cosigners: []common.Address{c1, c2}, cosignerThreshold: 2,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 2,
			wantErr: ErrInvalidBackupMetadata,
		},
		{
			name: "too many cosigners", admins: []types.PublicKey{pubA, pubB}, adminsThreshold: 2,
			cosigners: manyCosigners, cosignerThreshold: 2,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 2,
			wantErr: ErrInvalidBackupMetadata,
		},
		{
			name: "admin key not on curve", admins: []types.PublicKey{{X: common.HexToHash("01"), Y: common.HexToHash("02")}}, adminsThreshold: 1,
			cosigners: []common.Address{c1}, cosignerThreshold: 1,
			declared: []common.Address{addrA}, declaredThreshold: 1,
			wantErr: ErrMalformedPayload,
		},
		{
			name: "declared set does not match admins", admins: []types.PublicKey{pubA, pubB}, adminsThreshold: 2,
			cosigners: []common.Address{c1, c2}, cosignerThreshold: 2,
			declared: []common.Address{addrA}, declaredThreshold: 2,
			wantErr: ErrCosignerMismatch,
		},
		{
			name: "declared threshold does not match admins threshold", admins: []types.PublicKey{pubA, pubB}, adminsThreshold: 2,
			cosigners: []common.Address{c1, c2}, cosignerThreshold: 2,
			declared: []common.Address{addrA, addrB}, declaredThreshold: 1,
			wantErr: ErrCosignerThresholdMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := restoreData(t, backup.WalletBackupMetaData{
				AdminsPublicKeys:   tt.admins,
				AdminsThreshold:    tt.adminsThreshold,
				Cosigners:          tt.cosigners,
				CosignersThreshold: tt.cosignerThreshold,
			}, tt.declared, tt.declaredThreshold)

			cs, threshold, err := m.Cosigners(data)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, map[common.Address]bool{addrA: true, addrB: true}, cs)
			require.Equal(t, uint64(2), threshold)
		})
	}

	t.Run("unparseable metadata", func(t *testing.T) {
		_, _, err := m.Cosigners(&instruction.DataFixed{
			OPType:                 op.Wallet.Hash(),
			OPCommand:              op.KeyDataProviderRestore.Hash(),
			AdditionalFixedMessage: []byte("not json"),
			Cosigners:              []common.Address{addrA},
			CosignersThreshold:     1,
		})
		require.ErrorIs(t, err, ErrMalformedPayload)
	})
}
