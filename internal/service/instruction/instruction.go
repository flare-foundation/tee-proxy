package instruction

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	pkgvoting "github.com/flare-foundation/tee-proxy/pkg/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/flare-foundation/tee-node/pkg/processorutils"
)

var (
	errWrongTeeID       = fmt.Errorf("%w: wrong teeID", status.HTTP[400])
	errInvalidOP        = fmt.Errorf("%w: invalid pair opType, opCommand", status.HTTP[400])
	errInvalidSignature = fmt.Errorf("%w: invalid signature", status.HTTP[400])
	errRoundNotStored   = fmt.Errorf("%w: round not stored", status.HTTP[404])
	errNoInstruction    = fmt.Errorf("%w: no instruction with the provided id", status.HTTP[404])
)

// Service processes incoming instructions and manages threshold-based voting consensus.
type Service struct {
	teeID   common.Address
	chainID uint64

	vs *voting.Storage

	policies <-chan policy.SigningPolicy
	aq       *queue.ActionQueues

	metrics *metrics.Metrics
}

// NewService creates a new instruction Service with the given voting config, TEE identity, and dependencies.
// chainID is bound into the instruction signing hash and must match the TEE node's chain ID.
// m may be nil or disabled.
func NewService(ctx context.Context, votingCfg *config.Voting, teeID common.Address, policiesChan <-chan policy.SigningPolicy, aq *queue.ActionQueues, meta meta.Meta, chainID uint64, m *metrics.Metrics) *Service {
	vs := voting.NewStorage(ctx, votingCfg, meta, m)

	// Report current-epoch participant counts and the oldest resident signing-policy epoch at
	// scrape time; each is a no-op when its metric group is disabled.
	m.RegisterActiveDataProviderVoters(func() float64 { return float64(vs.CurrentRoundProviderVoterCount()) })
	m.RegisterActiveInitiators(func() float64 { return float64(vs.CurrentRoundInitiatorCount()) })
	m.RegisterTopUnfinalizedProposals(func() []metrics.ProviderPending {
		top := vs.CurrentRoundTopPending(3)
		out := make([]metrics.ProviderPending, len(top))
		for i, vp := range top {
			out[i] = metrics.ProviderPending{Provider: vp.Address.Hex(), Pending: float64(vp.Pending)}
		}
		return out
	})
	m.RegisterOldestStoredPolicy(func() float64 {
		o, ok := vs.OldestStoredEpoch()
		if !ok {
			return 0
		}
		return float64(o)
	})

	return &Service{
		teeID:   teeID,
		chainID: chainID,
		vs:      vs,

		policies: policiesChan,
		aq:       aq,

		metrics: m,
	}
}

// ServeInstruction accepts instruction, processes it, and returns the receipt.
//
// It checks that:
//   - correct teeID is set
//   - has a valid opType, opCommand pair
//
// Additional checks are done by the voting storage.
func (s *Service) ServeInstruction(_ context.Context, i *instruction.Instruction) (*pkgvoting.Receipt, error) {
	s.metrics.InstructionReceived()

	if i.Data.TeeID != s.teeID {
		s.metrics.InstructionRejected("wrong_tee_id")
		return nil, errWrongTeeID
	}

	ok := op.IsValidPair(i.Data.OPType, i.Data.OPCommand)
	if !ok {
		s.metrics.InstructionRejected("invalid_op")
		return nil, errInvalidOP
	}

	pub, err := i.RecoverSignersPubKey(s.chainID)
	if err != nil {
		s.metrics.InstructionRejected("invalid_signature")
		return nil, errInvalidSignature
	}
	signer := crypto.PubkeyToAddress(*pub)

	receipt, err := s.vs.AddVote(&i.Data, signer, i.Signature)
	if err != nil {
		s.metrics.InstructionRejected(voting.RejectReason(err))
		return nil, err
	}

	return receipt, nil
}

// Run starts the Forward and ListenToPolicies loops concurrently.
func (s *Service) Run(ctx context.Context) {
	go func() {
		if err := s.Forward(ctx); err != nil {
			logger.Errorf("instruction forward exited: %v", err)
		}
	}()
	if err := s.ListenToPolicies(ctx); err != nil {
		logger.Errorf("listen to policies exited: %v", err)
	}
}

// Forward listens to the out channel and enqueues received actions.
func (s *Service) Forward(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("instruction forwarding stopped: %w", ctx.Err())
		case action := <-s.vs.Out:
			err := s.aq.Enqueue(ctx, action, processorutils.Main)
			if err != nil {
				s.metrics.FinalizedActionEnqueueFailed()
				logger.Errorf("enqueuing instruction action: %v", err)
				continue
			}
		}
	}
}

// ListenToPolicies listens to policy channel and creates a new round when a new policy arrives.
func (s *Service) ListenToPolicies(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("policy listener stopped: %w", ctx.Err())
		case policy := <-s.policies:
			logger.Debugf("creating round for %d", policy.RewardEpochID)
			logger.Debugf("overwriting round for %d", int(policy.RewardEpochID)-s.vs.Size())
			s.vs.StoreNewRound(&policy)
		}
	}
}

// Statuses returns the statuses for instructionID if it is present in the given rewardEpochID.
func (s *Service) Statuses(instructionID common.Hash, rewardEpochID uint32) (*pkgvoting.Statuses, error) {
	r, exists := s.vs.Get(rewardEpochID)
	if !exists {
		return nil, errRoundNotStored
	}

	r.Voting.RLock()
	boxes, exists := r.Voting.M[instructionID]
	r.Voting.RUnlock()
	if !exists {
		return nil, errNoInstruction
	}

	boxes.RLock()
	defer boxes.RUnlock()

	status := make([]pkgvoting.Status, 0, len(boxes.M))
	for _, box := range boxes.M {
		status = append(status, box.Status())
	}

	return &pkgvoting.Statuses{
		InstructionID: instructionID,
		FinalizedHash: boxes.FinalizedHash,
		Status:        status,
	}, nil
}
