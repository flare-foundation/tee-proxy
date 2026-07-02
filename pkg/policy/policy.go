package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	chainID *big.Int,
) (*types.Action, *policy.SigningPolicy, int, error) {
	params := database.LatestLogsParams{
		Address: addresses.Relay,
		Topic0:  SigningPolicyInitializedEventSel,
		Number:  offset + 1,
	}

	logs, err := database.FetchLatestLogsByAddressAndTopic0(ctx, db, params)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("fetching signing policy logs: %w", err)
	}

	if len(logs) == 0 {
		return nil, nil, 0, errNoSigningPolicyLogs
	}
	event, err := policy.ParseSigningPolicyInitializedEvent(logs[len(logs)-1])
	if err != nil {
		return nil, nil, 0, fmt.Errorf("parsing signing policy event: %w", err)
	}

	p, err := policy.NewSigningPolicy(event, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("creating signing policy instance: %w", err)
	}

	msg, err := prepareInitializePolicyActionMessage(ctx, db, addresses.VoterRegistry, p, chainID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("preparing initialize policy message for reward epoch %d: %w", p.RewardEpochID, err)
	}

	action, err := prepareInitializePolicyAction(msg)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("preparing initialize policy action: %w", err)
	}

	return action, p, len(logs) - 1, nil
}

func prepareInitializePolicyAction(msg []byte) (*types.Action, error) {
	return queue.PrepareDirectAction(op.Policy, op.InitializePolicy, msg)
}

func prepareInitializePolicyActionMessage(ctx context.Context, db *gorm.DB, voterRegistryAddress common.Address, signingPolicy *policy.SigningPolicy, chainID *big.Int) ([]byte, error) {
	pubKeys := make([]types.PublicKey, len(signingPolicy.Voters.Voters()))
	for j, address := range signingPolicy.Voters.Voters() {
		if chainID.Cmp(costonChainID) == 0 && signingPolicy.RewardEpochID < costonRegistryMessageBreakingEpoch {
			voterRegistryAddress = costonOldVoterRegistryAddress
		}

		pk, err := recoverPubKey(ctx, db, address, signingPolicy.RewardEpochID, voterRegistryAddress, chainID)
		if err != nil {
			return nil, fmt.Errorf("recovering pub key for %v, on chain ID %v for epoch %v: %w", address, chainID, signingPolicy.RewardEpochID, err)
		}

		pubKeys[j] = types.PubKeyToStruct(pk)
	}

	req := &types.InitializePolicyRequest{
		InitialPolicyBytes: signingPolicy.RawBytes(),
		PublicKeys:         pubKeys,
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling initialize policy request: %w", err)
	}

	return encoded, nil
}

// FetchSigningPolicy fetches and parses the SigningPolicyInitialized event for the given policy ID from the database.
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
		return nil, fmt.Errorf("fetching signing policy %d logs: %w", signingPolicyID, err)
	}

	if len(logs) != 1 {
		return nil, errInvalidLogCount
	}

	event, err := policy.ParseSigningPolicyInitializedEvent(logs[0])
	if err != nil {
		return nil, fmt.Errorf("parsing signing policy %d event: %w", signingPolicyID, err)
	}

	p, err := policy.NewSigningPolicy(event, nil)
	if err != nil {
		return nil, fmt.Errorf("creating signing policy %d instance: %w", signingPolicyID, err)
	}

	return p, nil
}

// FetchSigningPolicyLog fetches the SigningPolicyInitialized event log for the given
// policy ID from the database. found is false (with a nil error) when no such event
// exists yet — i.e. the reward epoch has not been initialized on chain.
func FetchSigningPolicyLog(ctx context.Context, db *gorm.DB, relayAddress common.Address, signingPolicyID uint32) (database.Log, bool, error) {
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
		return database.Log{}, false, fmt.Errorf("fetching signing policy %d log: %w", signingPolicyID, err)
	}

	if len(logs) == 0 {
		return database.Log{}, false, nil
	}

	return logs[0], true, nil
}

// prepareSignatures transforms a slice of Signatures to a slice SignatureMessage.
func prepareSignatures(sigs []*registry.Signature) [][]byte {
	msgs := make([][]byte, len(sigs))

	for k := range sigs {
		msgs[k] = serializeSig(sigs[k])
	}

	return msgs
}

// UpdatePolicyAction prepares an UPDATE_POLICY action from a SigningPolicyInitialized log event.
func UpdatePolicyAction(ctx context.Context, db *gorm.DB, addresses config.Addresses, log database.Log, activePolicy *policy.SigningPolicy, chainID *big.Int) (*types.Action, *policy.SigningPolicy, error) {
	event, err := policy.ParseSigningPolicyInitializedEvent(log)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing signing policy event: %w", err)
	}

	p, err := policy.NewSigningPolicy(event, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("creating signing policy instance: %w", err)
	}

	msg, err := prepareUpdatePolicyMessage(ctx, db, addresses.FlareSystemsManager, addresses.VoterRegistry, p, activePolicy, int64(log.BlockNumber), chainID)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing update policy message: %w", err)
	}

	action, err := prepareUpdatePolicyAction(msg)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing update policy action: %w", err)
	}

	return action, p, nil
}

func prepareUpdatePolicyAction(msg []byte) (*types.Action, error) {
	return queue.PrepareDirectAction(op.Policy, op.UpdatePolicy, msg)
}

// policyRolloverSignatureDeadline caps how long collectSignatures waits for
// on-chain signNewSigningPolicy endorsements during a single attempt of a policy
// rollover. The update loop retries across attempts, so signatures that appear
// late are still collected on a later attempt; this only bounds one attempt so the
// loop can re-log progress and re-reconcile with the node. It is a var so tests can
// shrink it.
var policyRolloverSignatureDeadline = 30 * time.Minute

func prepareUpdatePolicyMessage(ctx context.Context, db *gorm.DB, flaresSystemManagerAddress, voterRegistryAddress common.Address, nextPolicy *policy.SigningPolicy, activePolicy *policy.SigningPolicy, start int64, chainID *big.Int) ([]byte, error) {
	deadline := time.Now().Add(policyRolloverSignatureDeadline)

	sigs, err := collectSignatures(ctx, db, flaresSystemManagerAddress, start, uint64(deadline.Unix()), nextPolicy, activePolicy)
	if err != nil {
		return nil, fmt.Errorf("collecting signatures: %w", err)
	}

	pubKeys := make([]types.PublicKey, len(nextPolicy.Voters.Voters()))

	for j, address := range nextPolicy.Voters.Voters() {
		vrAdd := voterRegistryAddress
		if chainID.Cmp(costonChainID) == 0 && nextPolicy.RewardEpochID < costonRegistryMessageBreakingEpoch {
			vrAdd = costonOldVoterRegistryAddress
		}

		pk, err := recoverPubKey(ctx, db, address, nextPolicy.RewardEpochID, vrAdd, chainID)
		if err != nil {
			return nil, fmt.Errorf("recovering pub key for %v: %w", address, err)
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
		return nil, fmt.Errorf("marshaling update policy request: %w", err)
	}

	return msg, nil
}
