package policy

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"gorm.io/gorm"
)

var (
	errNoSigningPolicyLogs = errors.New("no signing policy logs found in db")
	errInvalidLogCount     = errors.New("invalid number of logs")
)

// InitializePolicyAction prepares action for INITIALIZE_POLICY".
// SigningPolicyInitialized event that precedes the last emitted by offset is used.
// If such event is not found, the oldest found event is used.
//
// The action, signing policy, and used offset are returned.
func InitializePolicyAction(
	ctx context.Context,
	db *gorm.DB,
	addresses config.Addresses,
	offset int,
) (*types.Action, *policy.SigningPolicy, int, error) {
	params := database.LatestLogsParams{
		Address: addresses.Relay,
		Topic0:  SigningPolicyInitializedEventSel,
		Number:  offset + 1,
	}

	logs, err := database.FetchLatestLogsByAddressAndTopic0(ctx, db, params)
	if err != nil {
		return nil, nil, 0, err
	}

	if len(logs) == 0 {
		return nil, nil, 0, errNoSigningPolicyLogs
	}
	event, err := policy.ParseSigningPolicyInitializedEvent(logs[len(logs)-1])
	if err != nil {
		return nil, nil, 0, err
	}

	p := policy.NewSigningPolicy(event, nil)

	chainID := new(big.Int).SetUint64(addresses.ChainID)
	msg, err := prepareInitializePolicyActionMessage(ctx, db, addresses.VoterRegistry, p, chainID)
	if err != nil {
		return nil, nil, 0, err
	}

	action, err := prepareInitializePolicyAction(msg)
	if err != nil {
		return nil, nil, 0, err
	}

	return action, p, len(logs) - 1, nil
}

func prepareInitializePolicyAction(msg []byte) (*types.Action, error) {
	return queue.PrepareDirectAction(op.Policy, op.InitializePolicy, msg)
}

func prepareInitializePolicyActionMessage(ctx context.Context, db *gorm.DB, voterRegistryAddress common.Address, signingPolicy *policy.SigningPolicy, chainID *big.Int) ([]byte, error) {
	pubKeys := make([]types.PublicKey, len(signingPolicy.Voters.Voters()))
	for j, address := range signingPolicy.Voters.Voters() {
		pk, err := recoverPubKey(ctx, db, address, signingPolicy.RewardEpochID, voterRegistryAddress, chainID)
		if err != nil {
			return nil, err
		}

		pubKeys[j] = types.PubKeyToStruct(pk)
	}

	req := &types.InitializePolicyRequest{
		InitialPolicyBytes: signingPolicy.RawBytes(),
		PublicKeys:         pubKeys,
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	return encoded, nil
}

func FetchSigningPolicy(ctx context.Context, db *gorm.DB, relayAddress common.Address, signingPolicyID uint32) (*policy.SigningPolicy, error) {
	topics := [4]common.Hash{}
	topics[0] = SigningPolicyInitializedEventSel
	topics[1] = convert.Uint32ToHash(signingPolicyID)

	params := database.LogsFullParams{
		Address: relayAddress,
		Topics:  topics,
		Number:  1,
	}

	logs, err := database.FetchLogsFull(ctx, db, params)
	if err != nil {
		return nil, err
	}

	if len(logs) != 1 {
		return nil, errInvalidLogCount
	}

	event, err := policy.ParseSigningPolicyInitializedEvent(logs[0])
	if err != nil {
		return nil, err
	}

	return policy.NewSigningPolicy(event, nil), nil
}

// prepareSignatures transforms a slice of Signatures to a slice SignatureMessage.
func prepareSignatures(sigs []*registry.Signature) [][]byte {
	msgs := make([][]byte, len(sigs))

	for k := range sigs {
		msgs[k] = serializeSig(sigs[k])
	}

	return msgs
}

func UpdatePolicyAction(ctx context.Context, db *gorm.DB, addresses config.Addresses, log database.Log, activePolicy *policy.SigningPolicy) (*types.Action, *policy.SigningPolicy, error) {
	event, err := policy.ParseSigningPolicyInitializedEvent(log)
	if err != nil {
		return nil, nil, err
	}

	p := policy.NewSigningPolicy(event, nil)

	chainID := new(big.Int).SetUint64(addresses.ChainID)
	msg, err := prepareUpdatePolicyMessage(ctx, db, addresses.FlareSystemsManager, addresses.VoterRegistry, p, activePolicy, int64(log.BlockNumber), chainID)
	if err != nil {
		return nil, nil, err
	}

	action, err := prepareUpdatePolicyAction(msg)
	if err != nil {
		return nil, nil, err
	}

	return action, p, nil
}

func prepareUpdatePolicyAction(msg []byte) (*types.Action, error) {
	return queue.PrepareDirectAction(op.Policy, op.UpdatePolicy, msg)
}

func prepareUpdatePolicyMessage(ctx context.Context, db *gorm.DB, flaresSystemManagerAddress, voterRegistryAddress common.Address, nextPolicy *policy.SigningPolicy, activePolicy *policy.SigningPolicy, start int64, chainID *big.Int) ([]byte, error) {
	deadline := time.Now().Add(3 * time.Hour) // todo

	sigs, err := collectSignatures(ctx, db, flaresSystemManagerAddress, start, uint64(deadline.Unix()), nextPolicy, activePolicy)
	if err != nil {
		return nil, err
	}

	pubKeys := make([]types.PublicKey, len(nextPolicy.Voters.Voters()))

	for j, address := range nextPolicy.Voters.Voters() {
		pk, err := recoverPubKey(ctx, db, address, nextPolicy.RewardEpochID, voterRegistryAddress, chainID)
		if err != nil {
			return nil, err
		}

		pubKeys[j] = types.PubKeyToStruct(pk)
	}

	req := types.UpdatePolicyRequest{
		NewPolicy: types.MultiSignedPolicy{
			PolicyBytes: nextPolicy.RawBytes(),
			Signatures:  prepareSignatures(sigs),
		},
		PublicKeys: pubKeys,
	}

	msg, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	return msg, nil
}
