package voting

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction/voting/limiter"
	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

type rejectReasonCase struct {
	name string
	err  error
	want string
}

// rejectReasonCases covers one representative error per RejectReason branch, each wrapped as
// its production emit site wraps it. It is the source of truth for both TestRejectReason and
// the pre-init sync guard, so a new reason forces a case here.
var rejectReasonCases = []rejectReasonCase{
	{"invalid voter", errInvalidVoter, "invalid_voter"},
	{"voting ended", errVotingEnded, "voting_ended"},
	{"duplicate signature", errSignatureAlreadyStored, "duplicate_signature"},
	{"voting before event", errVotingBeforeEvent, "event_in_future"},
	// errVotingEnded as produced wrapped for a deleted box (voting.go).
	{"voting ended, deleted box wrap", fmt.Errorf("%w: id", errVotingEnded), "voting_ended"},
	// Any box error as wrapped by AddVote (voting.go).
	{"invalid voter, AddVote wrap", fmt.Errorf("adding vote from x to y: %w", errInvalidVoter), "invalid_voter"},

	// Reasons split out of the "other" collapse, each wrapped as its emit site wraps it.
	{"no round", fmt.Errorf("%w %d", errNoRound, 42), "no_round"},
	{"inconsistent data", fmt.Errorf("%w: %v", errInconsistentData, errors.New("verifying FDC signature")), "inconsistent"},
	{"rate limited", limiter.ErrLimitReached, "rate_limited"},
	{"not eligible", limiter.ErrCannotInitialize, "not_eligible"},
	// box's own cosigner-threshold wrap (buildVoteBox), distinct from meta's declaration mismatch.
	{"cosigner threshold wrap", fmt.Errorf("%w: %d > %d", errInvalidCosignerThreshold, 5, 3), "invalid_cosigner_threshold"},
	// meta cosigner errors as surfaced by buildVoteBox ("reading cosigners: %w").
	{"cosigner set mismatch", fmt.Errorf("reading cosigners: %w", meta.ErrCosignerMismatch), "invalid_cosigner_declaration"},
	{"cosigner threshold mismatch", fmt.Errorf("reading cosigners: %w", meta.ErrCosignerThresholdMismatch), "invalid_cosigner_declaration"},
	{"duplicate cosigners", fmt.Errorf("reading cosigners: %w", meta.ErrDuplicateCosigners), "duplicate_cosigners"},
	{"invalid backup metadata", fmt.Errorf("reading cosigners: %w", fmt.Errorf("%w: duplicate admin addresses", meta.ErrInvalidBackupMetadata)), "invalid_backup_metadata"},
	// malformed payload from meta.Cosigners resolution, wrapped by meta then by buildVoteBox.
	{"malformed payload", fmt.Errorf("reading cosigners: %w", fmt.Errorf("%w: parsing payment instruction: %v", meta.ErrMalformedPayload, errors.New("bad rlp"))), "malformed_payload"},
	// unknown wallet from walletCosigners->WalletInfo, wrapped by buildVoteBox ("reading cosigners: %w").
	{"unknown wallet", fmt.Errorf("reading cosigners: %w", wallets.ErrWalletNotFound), "unknown_wallet"},
	// meta FDC threshold errors as surfaced by buildVoteBox ("reading threshold: %w").
	{"fdc threshold too low", fmt.Errorf("reading threshold: %w", meta.ErrFDCThresholdTooLow), "invalid_fdc_threshold"},
	{"fdc threshold below half", fmt.Errorf("reading threshold: %w", meta.ErrFDCThresholdBelowHalf), "invalid_fdc_threshold"},
	{"fdc threshold too high", fmt.Errorf("reading threshold: %w", meta.ErrFDCThresholdTooHigh), "invalid_fdc_threshold"},
	// checkSize errors returned directly by AddVote.
	{"too many cosigners", errTooManyCosigners, "oversized"},
	{"original message too big", errOriginalMessageTooBig, "oversized"},
	{"additional message too big", errAdditionalMessageTooBig, "oversized"},
	{"additional variable too big", errAdditionalVariableTooBig, "oversized"},
	{"non instruction command", errNonInstructionCommand, "non_instruction_command"},
	// buildVoteBox's guard on a policy whose threshold cannot have come from a chain event.
	{"zero policy threshold", errZeroPolicyThreshold, "zero_policy_threshold"},

	{"unmatched collapses to other", errors.New("boom"), "other"},
}

// TestRejectReason pins the bounded label set, including the "other" collapse for unmatched
// errors, and proves the errors.Is chains survive the wrapping the production call path applies
// (each case wraps the sentinel the way its emit site does).
func TestRejectReason(t *testing.T) {
	for _, tt := range rejectReasonCases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, RejectReason(tt.err))
		})
	}
}

// instructionServiceReasons are the reasons set in the instruction service before voting
// (instruction.go), which RejectReason never produces but the metrics pre-init must include.
var instructionServiceReasons = []string{"wrong_tee_id", "invalid_op", "invalid_signature"}

// TestInstructionRejectedPreInitMatchesRejectReason enforces the keep-in-sync contract between
// RejectReason's label set and the metrics pre-initialization: the pre-initialized series must
// equal exactly the reasons RejectReason can return plus the instruction-service reasons — no
// missing baseline, no phantom always-zero series.
func TestInstructionRejectedPreInitMatchesRejectReason(t *testing.T) {
	want := map[string]bool{}
	for _, r := range instructionServiceReasons {
		want[r] = true
	}
	for _, c := range rejectReasonCases {
		want[RejectReason(c.err)] = true
	}

	m := metrics.New(metrics.Config{Enable: true, Voting: true})

	families, err := m.Registry().Gather()
	require.NoError(t, err)

	got := map[string]bool{}
	for _, f := range families {
		if f.GetName() != "teeproxy_instructions_rejected_total" {
			continue
		}
		for _, series := range f.GetMetric() {
			for _, l := range series.GetLabel() {
				if l.GetName() == "reason" {
					got[l.GetValue()] = true
				}
			}
		}
	}

	require.Equal(t, want, got)
}

// TestClientRejectionStatusCodes pins the client-facing HTTP status of the classified rejection
// sentinels that carry no obvious inline status, so a future "simplify to errors.New" cannot
// silently regress them to 500. Status resolves through the emit-site %w wrap.
func TestClientRejectionStatusCodes(t *testing.T) {
	tests := []struct {
		err  error
		want int // 400 bad request, 404 not found
	}{
		{meta.ErrCosignerMismatch, 400},
		{meta.ErrCosignerThresholdMismatch, 400},
		{meta.ErrMalformedPayload, 400},
		{meta.ErrFDCThresholdTooLow, 400},
		{meta.ErrFDCThresholdBelowHalf, 400},
		{meta.ErrFDCThresholdTooHigh, 400},
		{wallets.ErrWalletNotFound, 404},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, status.ErrToCode(tt.err))
		require.Equal(t, tt.want, status.ErrToCode(fmt.Errorf("reading cosigners: %w", tt.err)))
	}
}
