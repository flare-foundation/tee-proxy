package testutil

import (
	"crypto/ecdsa"
	"encoding/hex"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/system"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var rowCounter atomic.Uint64

// LogRow is an event log as the C-chain indexer stores it.
type LogRow struct {
	Address   common.Address
	Event     abi.Event
	Topics    []common.Hash // indexed values after topic0
	Args      []any         // non-indexed values
	Block     uint64
	Timestamp uint64
	TxHash    common.Hash // zero: a unique hash is generated
	Index     uint64
}

// Data is r's ABI-encoded non-indexed values.
func (r LogRow) Data(t *testing.T) []byte {
	t.Helper()

	data, err := r.Event.Inputs.NonIndexed().Pack(r.Args...)
	require.NoError(t, err)

	return data
}

// InsertLog stores r in db, which must have database.Log migrated.
func InsertLog(t *testing.T, db *gorm.DB, r LogRow) {
	t.Helper()

	data := r.Data(t)

	topics := [3]string{}
	for i, h := range r.Topics {
		topics[i] = hex.EncodeToString(h[:])
	}

	require.NoError(t, db.Create(&database.Log{
		Address:         hex.EncodeToString(r.Address[:]),
		Data:            hex.EncodeToString(data),
		Topic0:          hex.EncodeToString(r.Event.ID[:]),
		Topic1:          topics[0],
		Topic2:          topics[1],
		Topic3:          topics[2],
		TransactionHash: hashOrUnique(r.TxHash),
		LogIndex:        r.Index,
		Timestamp:       r.Timestamp,
		BlockNumber:     r.Block,
	}).Error)
}

// TxRow is a transaction as the C-chain indexer stores it.
type TxRow struct {
	To        common.Address
	Input     []byte // selector + ABI-encoded arguments
	Block     uint64
	Index     uint64
	Timestamp uint64
	Status    uint64
	Hash      common.Hash // zero: a unique hash is generated
}

// InsertTx stores r in db, which must have database.Transaction migrated.
func InsertTx(t *testing.T, db *gorm.DB, r TxRow) {
	t.Helper()

	require.GreaterOrEqual(t, len(r.Input), 4)
	require.NoError(t, db.Create(&database.Transaction{
		Hash:             hashOrUnique(r.Hash),
		FunctionSig:      hex.EncodeToString(r.Input[:4]),
		Input:            hex.EncodeToString(r.Input),
		BlockNumber:      r.Block,
		TransactionIndex: r.Index,
		ToAddress:        hex.EncodeToString(r.To[:]),
		Status:           r.Status,
		Timestamp:        r.Timestamp,
	}).Error)
}

// InsertState stores the indexer's last_database_block state; db must have database.State migrated.
func InsertState(t *testing.T, db *gorm.DB, index, timestamp uint64) {
	t.Helper()

	require.NoError(t, db.Create(&database.State{
		Name:           "last_database_block",
		Index:          index,
		BlockTimestamp: timestamp,
	}).Error)
}

func hashOrUnique(h common.Hash) string {
	if h == (common.Hash{}) {
		h = common.BigToHash(new(big.Int).SetUint64(rowCounter.Add(1)))
	}
	return hex.EncodeToString(h[:])
}

// PolicyEvent returns a SigningPolicyInitialized event for epoch with the given
// signing-policy keys and weights.
func PolicyEvent(epoch uint32, keys []*ecdsa.PrivateKey, weights []uint16, threshold uint16) *relay.RelaySigningPolicyInitialized {
	voters := make([]common.Address, len(keys))
	for i, k := range keys {
		voters[i] = crypto.PubkeyToAddress(k.PublicKey)
	}

	return &relay.RelaySigningPolicyInitialized{
		RewardEpochId:      new(big.Int).SetUint64(uint64(epoch)),
		StartVotingRoundId: epoch * 10,
		Threshold:          threshold,
		Seed:               big.NewInt(int64(epoch)),
		Voters:             voters,
		Weights:            weights,
		SigningPolicyBytes: []byte{byte(epoch)},
		Timestamp:          uint64(epoch),
	}
}

// InsertPolicyEvent stores ev as a SigningPolicyInitialized log emitted by relayAddress.
func InsertPolicyEvent(t *testing.T, db *gorm.DB, relayAddress common.Address, ev *relay.RelaySigningPolicyInitialized, block uint64) {
	t.Helper()
	InsertLog(t, db, PolicyEventLog(t, relayAddress, ev, block))
}

// PolicyEventLog is ev as a SigningPolicyInitialized log emitted by relayAddress.
func PolicyEventLog(t *testing.T, relayAddress common.Address, ev *relay.RelaySigningPolicyInitialized, block uint64) LogRow {
	t.Helper()

	relayABI, err := relay.RelayMetaData.GetAbi()
	require.NoError(t, err)

	return LogRow{
		Address:   relayAddress,
		Event:     relayABI.Events["SigningPolicyInitialized"],
		Topics:    []common.Hash{common.BigToHash(ev.RewardEpochId)},
		Args:      []any{ev.StartVotingRoundId, ev.Threshold, ev.Seed, ev.Voters, ev.Weights, ev.SigningPolicyBytes, ev.Timestamp},
		Block:     block,
		Timestamp: block,
	}
}

// InsertRegistration stores RegistrationLog in db.
func InsertRegistration(t *testing.T, db *gorm.DB, registryAddress common.Address, chainID uint64, epoch uint32, voter common.Address, key, signer *ecdsa.PrivateKey, block uint64) {
	t.Helper()
	InsertLog(t, db, RegistrationLog(t, registryAddress, chainID, epoch, voter, key, signer, block))
}

// RegistrationLog is a VoterRegistered log for the signing-policy key in epoch,
// signed with signer as the VoterRegistry expects:
// keccak256(abi.encode(chainID, epoch, voter)), EIP-191 prefixed.
func RegistrationLog(t *testing.T, registryAddress common.Address, chainID uint64, epoch uint32, voter common.Address, key, signer *ecdsa.PrivateKey, block uint64) LogRow {
	t.Helper()

	registryABI, err := registry.RegistryMetaData.GetAbi()
	require.NoError(t, err)

	uint256Ty, err := abi.NewType("uint256", "", nil)
	require.NoError(t, err)
	uint32Ty, err := abi.NewType("uint32", "", nil)
	require.NoError(t, err)
	addressTy, err := abi.NewType("address", "", nil)
	require.NoError(t, err)
	msg, err := abi.Arguments{{Type: uint256Ty}, {Type: uint32Ty}, {Type: addressTy}}.Pack(new(big.Int).SetUint64(chainID), epoch, voter)
	require.NoError(t, err)

	sig := sign(t, accounts.TextHash(crypto.Keccak256(msg)), signer)
	spAddress := crypto.PubkeyToAddress(key.PublicKey)

	return LogRow{
		Address: registryAddress,
		Event:   registryABI.Events["VoterRegistered"],
		Topics:  []common.Hash{common.BytesToHash(voter[:]), common.BigToHash(big.NewInt(int64(epoch))), common.BytesToHash(spAddress[:])},
		Args: []any{
			voter, voter,
			registry.PublicKey{},
			big.NewInt(1),
			registry.Signature{V: sig.V, R: sig.R, S: sig.S},
		},
		Block:     block,
		Timestamp: block,
	}
}

// SignNewSigningPolicyInput returns signNewSigningPolicy calldata for epoch carrying
// key's EIP-191 signature over hash.
func SignNewSigningPolicyInput(t *testing.T, epoch uint32, hash common.Hash, key *ecdsa.PrivateKey) []byte {
	t.Helper()

	fsmABI, err := system.FlareSystemsManagerMetaData.GetAbi()
	require.NoError(t, err)
	method := fsmABI.Methods["signNewSigningPolicy"]

	sig := sign(t, accounts.TextHash(hash[:]), key)
	packed, err := method.Inputs.Pack(new(big.Int).SetUint64(uint64(epoch)), hash, sig)
	require.NoError(t, err)

	return append(append([]byte{}, method.ID...), packed...)
}

// sign returns key's signature over digest with Ethereum-style v in {27, 28}.
func sign(t *testing.T, digest []byte, key *ecdsa.PrivateKey) registry.Signature {
	t.Helper()

	raw, err := crypto.Sign(digest, key)
	require.NoError(t, err)

	var sig registry.Signature
	copy(sig.R[:], raw[:32])
	copy(sig.S[:], raw[32:64])
	sig.V = raw[64] + 27

	return sig
}

// SerializedSignature is the [R||S||V-27] form the proxy forwards to the TEE node.
func SerializedSignature(sig registry.Signature) []byte {
	out := make([]byte, 0, 65)
	out = append(out, sig.R[:]...)
	out = append(out, sig.S[:]...)
	return append(out, sig.V-27)
}

// PolicySignature is key's signature over hash as signNewSigningPolicy carries it.
func PolicySignature(t *testing.T, hash common.Hash, key *ecdsa.PrivateKey) registry.Signature {
	t.Helper()
	return sign(t, accounts.TextHash(hash[:]), key)
}
