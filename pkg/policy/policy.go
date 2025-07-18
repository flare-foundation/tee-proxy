package policy

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"gorm.io/gorm"
)

func blockToVotingRoundID(database.Block) uint32 {
	return 0 // todo
}

func InitializePolicyAction(
	ctx context.Context,
	db *gorm.DB,
	addresses config.Addresses,
) (*types.Action, *policy.SigningPolicy, error) {
	logs, err := fetchLastTwoSigningPolicyInitializedEvents(ctx, db, addresses.Relay)
	if err != nil {
		return nil, nil, err
	}

	latestBlock, err := database.FetchLatestBlock(ctx, db, nil)
	if err != nil {
		return nil, nil, err
	}

	event, err := policy.ParseSigningPolicyInitializedEvent(logs[0])
	if err != nil {
		return nil, nil, err
	}

	if blockToVotingRoundID(latestBlock) < event.StartVotingRoundId {
		event, err = policy.ParseSigningPolicyInitializedEvent(logs[1])
		if err != nil {
			return nil, nil, err
		}
	}

	p := policy.NewSigningPolicy(event, nil)

	msg, err := prepareInitializePolicyActionMessage(ctx, db, addresses.VoterRegistry, p)
	if err != nil {
		return nil, nil, err
	}

	action, err := prepareInitializePolicyAction(msg)
	if err != nil {
		return nil, nil, err
	}

	return action, p, err
}

// fetchLastTwoSigningPolicyInitializedEvents fetches last two SigningPolicyInitialized
// events emitted by Relay.
func fetchLastTwoSigningPolicyInitializedEvents(
	ctx context.Context,
	db *gorm.DB,
	relayAddress common.Address,
) ([]database.Log, error) {
	params := database.LatestLogsParams{
		Address: relayAddress,
		Topic0:  signingPolicyInitializedEventSel,
		Number:  2,
	}
	return database.FetchLatestLogsByAddressAndTopic0(ctx, db, params)
}

func prepareInitializePolicyAction(msg []byte) (*types.Action, error) {
	return queue.PrepareDirectAction(constants.Policy, constants.InitializePolicy, msg)
}

func prepareInitializePolicyActionMessage(ctx context.Context, db *gorm.DB, voterRegistryAddress common.Address, signingPolicy *policy.SigningPolicy) ([]byte, error) {
	pubKeys := make([]types.ECDSAPublicKey, len(signingPolicy.Voters.Voters()))
	for j, address := range signingPolicy.Voters.Voters() {
		pk, err := recoverPubKey(ctx, db, address, uint32(signingPolicy.RewardEpochID), voterRegistryAddress)
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
	topics[0] = signingPolicyInitializedEventSel
	topics[1] = Uint32ToHash(signingPolicyID)

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
		return nil, errors.New("invalid number of logs")
	}

	event, err := policy.ParseSigningPolicyInitializedEvent(logs[0])
	if err != nil {
		return nil, err
	}

	return policy.NewSigningPolicy(event, nil), nil
}

func SigningPolicyInitializedEventsListener(
	ctx context.Context,
	db *gorm.DB,
	relayAddress common.Address,
	startPolicyID uint32,
) (<-chan []database.Log, error) {
	out := make(chan []database.Log, 1)

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			topics := [4]common.Hash{}
			topics[0] = signingPolicyInitializedEventSel
			topics[1] = Uint32ToHash(startPolicyID)

			params := database.LogsFullParams{
				Address: relayAddress,
				Topics:  topics,
				Number:  1,
			}

			logs, err := database.FetchLogsFull(ctx, db, params)
			if err != nil {
				logger.Error("fetch logs error:", err)
				continue
			}

			if len(logs) > 0 {
				out <- logs
				startPolicyID++
				continue
			}

			time.Sleep(10 * time.Minute)
		}
	}()

	return out, nil
}

// joinSigsAndKeys joins slices of signatures and public keys into slice of SignatureMessages.
//
// It is assumed that signs and keys have the same length.
func joinSigsAndKeys(sigs []*registry.Signature, keys []*ecdsa.PublicKey) []*types.SignatureMessage {
	msgs := make([]*types.SignatureMessage, len(sigs))

	for k := range sigs {
		sm := types.SignatureMessage{
			Signature: serializeSig(sigs[k]),
			PublicKey: types.PubKeyToStruct(keys[k]),
		}

		msgs[k] = &sm
	}

	return msgs
}

func UpdatePolicyAction(ctx context.Context, db *gorm.DB, addresses config.Addresses, log database.Log, activePolicy *policy.SigningPolicy) (*types.Action, *policy.SigningPolicy, error) {
	event, err := policy.ParseSigningPolicyInitializedEvent(log)
	if err != nil {
		return nil, nil, err
	}

	p := policy.NewSigningPolicy(event, nil)

	msg, err := prepareUpdatePolicyMessage(ctx, db, addresses.FlareSystemsManager, addresses.VoterRegistry, p, activePolicy, int64(log.BlockNumber))
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
	return queue.PrepareDirectAction(constants.Policy, constants.UpdatePolicy, msg)
}

func prepareUpdatePolicyMessage(ctx context.Context, db *gorm.DB, flaresSystemManagerAddress, voterRegistryAddress common.Address, nextPolicy *policy.SigningPolicy, activePolicy *policy.SigningPolicy, start int64) ([]byte, error) {
	deadline := time.Now().Add(3 * time.Hour) // todo

	sigs, keys, err := collectSignatures(ctx, db, flaresSystemManagerAddress, start, uint64(deadline.Unix()), nextPolicy, activePolicy)
	if err != nil {
		return nil, err
	}

	pubKeys := make([]types.ECDSAPublicKey, len(nextPolicy.Voters.Voters()))

	for j, address := range nextPolicy.Voters.Voters() {
		pk, err := recoverPubKey(ctx, db, address, uint32(nextPolicy.RewardEpochID), voterRegistryAddress)
		if err != nil {
			return nil, err
		}

		pubKeys[j] = types.PubKeyToStruct(pk)
	}

	req := types.UpdatePolicyRequest{
		NewPolicy: types.MultiSignedPolicy{
			PolicyBytes: nextPolicy.RawBytes(),
			Signatures:  joinSigsAndKeys(sigs, keys),
		},
		PublicKeys: pubKeys,
	}

	msg, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	return msg, nil
}
