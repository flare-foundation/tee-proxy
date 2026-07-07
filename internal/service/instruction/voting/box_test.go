package voting

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRejectReason pins the bounded label set, including the "other" collapse for
// unmatched errors, and proves the errors.Is chains survive the wrapping the
// production call path applies (voting.go AddVote wraps box errors with %w).
func TestRejectReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"invalid voter", errInvalidVoter, "invalid_voter"},
		{"voting ended", errVotingEnded, "voting_ended"},
		{"duplicate signature", errSignatureAlreadyStored, "duplicate_signature"},
		{"voting before event", errVotingBeforeEvent, "event_in_future"},
		// errVotingEnded as produced wrapped at voting.go:162.
		{"voting ended, deleted box wrap", fmt.Errorf("%w: id", errVotingEnded), "voting_ended"},
		// Any box error as wrapped by AddVote at voting.go:172.
		{"invalid voter, AddVote wrap", fmt.Errorf("adding vote from x to y: %w", errInvalidVoter), "invalid_voter"},
		{"unmatched collapses to other", errors.New("boom"), "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, RejectReason(tt.err))
		})
	}
}
