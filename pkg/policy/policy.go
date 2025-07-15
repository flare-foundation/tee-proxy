package policy

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
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
	"gorm.io/gorm"
)

// fetchSigningPolicyInitializedEventLogs fetches all signingPolicyInitialized event logs
// emitted by Relay with signingPolicyID higher or equal to initialSigningPolicyID.
func FetchSigningPolicyInitializedEvents(
	ctx context.Context,
	db *gorm.DB,
	relayAddress common.Address,
	initialSigningPolicyID uint32,
) ([]database.Log, error) {
	var logs []database.Log

	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 >= ?",
		AddressToQueryParam(relayAddress),
		hex.EncodeToString(signingPolicyInitializedEventSel[:]),
		hex.EncodeToString(Uint32ToHash(initialSigningPolicyID).Bytes()),
	).Order("timestamp").Find(&logs).Error // todo add retry

	return logs, err
}

func prepareInitializePolicyAction(id common.Hash, msg []byte) (*types.Action, error) {
	di := types.DirectInstruction{
		Data: types.DirectInstructionData{
			OPType:    constants.Policy.Hash(),
			OPCommand: constants.InitializePolicy.Hash(),
			Message:   msg,
		},
		Signatures: nil,
	}

	edi, err := json.Marshal(di)
	if err != nil {
		return nil, err
	}

	a := &types.Action{
		Data: types.ActionData{
			ID:            id,
			Type:          types.Direct,
			SubmissionTag: types.Submit,
			Message:       edi,
		},
		AdditionalVariableMessages: nil,
		Timestamps:                 nil,
		AdditionalActionData:       nil,
		Signatures:                 nil,
	}

	return a, nil
}

func prepareInitializePolicyActionMessage(ctx context.Context, db *gorm.DB, flaresSystemManagerAddress, voterRegistryAddress common.Address, logs []database.Log) ([]byte, error) {
	initialEvent, err := policy.ParseSigningPolicyInitializedEvent(logs[0])
	if err != nil {
		return nil, err
	}

	logs[0] = database.Log{}

	logs = logs[1:]

	previousPolicy := policy.NewSigningPolicy(initialEvent, nil)

	req := &types.InitializePolicyRequest{
		InitialPolicyBytes: previousPolicy.RawBytes(),
		Policies:           make([]types.MultiSignedPolicy, 0, len(logs)+1),
	}

	for j := range len(logs) - 1 {
		p, mp, err := prepareSignedPolicy(ctx, db, flaresSystemManagerAddress, logs[j], previousPolicy, int64(logs[j+1].BlockNumber))
		if err != nil {
			return nil, err
		}

		previousPolicy = p

		req.Policies = append(req.Policies, *mp)
	}

	event, err := policy.ParseSigningPolicyInitializedEvent(logs[len(logs)-1])
	if err != nil {
		return nil, err
	}

	p := policy.NewSigningPolicy(event, nil)

	deadline := time.Now().Add(3 * time.Hour).Unix() // todo improve/ argue this

	sigs, keys, err := collectSignatures(ctx, db, flaresSystemManagerAddress, int64(logs[len(logs)-1].Transaction.BlockNumber), uint64(deadline), *p, *previousPolicy)
	if err != nil {
		return nil, err
	}

	mp := &types.MultiSignedPolicy{
		PolicyBytes: p.RawBytes(),
		Signatures:  joinSigsAndKeys(sigs, keys),
	}

	req.Policies = append(req.Policies, *mp)

	pubKeys := make([]types.ECDSAPublicKey, 100)

	for j, address := range p.Voters.Voters() {
		pk, err := recoverPubKey(ctx, db, address, uint32(p.RewardEpochID), voterRegistryAddress)
		if err != nil {
			return nil, err
		}

		pubKeys[j] = types.PubKeyToStruct(pk)
	}

	req.LatestPolicyPublicKeys = pubKeys

	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	return encoded, nil
}

func SigningPolicyInitializedEventsListener(
	ctx context.Context,
	db *gorm.DB,
	relayAddress common.Address,
	activeSigningPolicyID uint32,
	out chan []database.Log,
) error {
	var logs []database.Log

	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 == ?",
		AddressToQueryParam(relayAddress),
		hex.EncodeToString(signingPolicyInitializedEventSel[:]),
		hex.EncodeToString(Uint32ToHash(activeSigningPolicyID).Bytes()),
	).Order("timestamp").Find(&logs).Error // todo add retry
	if err != nil {
		return err
	}
	if len(logs) == 0 {
		return errors.New("no logs received")
	}

	fromBlock := int64(logs[0].BlockNumber)

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			state, err := database.FetchState(ctx, db, nil)
			if err != nil {
				logger.Panic("fetch initial state error:", err)
				continue
			}

			params := database.LogsParams{
				Address: relayAddress,
				Topic0:  signingPolicyInitializedEventSel,
				From:    fromBlock,
				To:      int64(state.Index),
			}

			logs, err := database.FetchLogsByAddressAndTopic0BlockNumber(
				ctx, db, params,
			)
			if err != nil {
				logger.Error("fetch logs error:", err)
				continue
			}

			if len(logs) > 0 {
				out <- logs
			}

			fromBlock = int64(state.Index)

			time.Sleep(10 * time.Minute)
		}
	}()

	return nil
}

func prepareSignedPolicy(ctx context.Context, db *gorm.DB, flaresSystemManagerAddress common.Address, log database.Log, previousPolicy *policy.SigningPolicy, toBlock int64) (*policy.SigningPolicy, *types.MultiSignedPolicy, error) {
	event, err := policy.ParseSigningPolicyInitializedEvent(log)
	if err != nil {
		return nil, nil, err
	}

	p := policy.NewSigningPolicy(event, nil)

	sigs, keys, err := fetchSignatures(ctx, db, flaresSystemManagerAddress, int64(log.BlockNumber), toBlock, p, previousPolicy) // todo: toBlock have to be computed.
	if err != nil {
		return nil, nil, err
	}

	mp := &types.MultiSignedPolicy{
		PolicyBytes: p.RawBytes(),
		Signatures:  joinSigsAndKeys(sigs, keys),
	}

	return p, mp, nil
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
