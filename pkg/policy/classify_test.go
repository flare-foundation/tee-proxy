package policy

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateFailureReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"pubkey_mismatch wrapped", fmt.Errorf("preparing update policy message: %w", fmt.Errorf("recovering pub key: %w", errWrongAddressRecovered)), "pubkey_mismatch"},
		{"sig_deadline wrapped", fmt.Errorf("collecting signatures: %w", ErrDeadlineExceeded), "sig_deadline"},
		{"indexer too many errors", fmt.Errorf("collecting signatures: %w", ErrTooManyErrors), "indexer"},
		{"indexer pubkey not indexed", fmt.Errorf("recovering pub key: %w", errInvalidLogCountPubKeys), "indexer"},
		{"not_consecutive", fmt.Errorf("collecting signatures: %w", errNotConsecutivePolicy), "not_consecutive"},
		{"residual build_failed", errors.New("marshaling request failed"), "build_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, UpdateFailureReason(tt.err))
		})
	}
}
