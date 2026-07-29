package attestation

import (
	"errors"
	"fmt"
	"testing"

	teeattestation "github.com/flare-foundation/tee-node/pkg/attestation"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil is ok", nil, "ok"},
		{"challenge", ErrChallengeMismatch, "challenge_mismatch"},
		{"pubkey", ErrPubKeyMismatch, "pubkey_mismatch"},
		{"chain id", ErrChainIDMismatch, "chain_id_mismatch"},
		{"magic pass disabled", ErrMagicPassDisabled, "magic_pass_disabled"},
		{"token too old (wrapped)", fmt.Errorf("ctx: %w", ErrTokenTooOld), "token_too_old"},
		{"sec boot", ErrSecBootDisabled, "sec_boot_disabled"},
		{"debug", ErrDebugNotAllowed, "debug_not_allowed"},
		{"code hash", ErrCodeHashNotAllowed, "code_hash_not_allowed"},
		{"platform", ErrPlatformNotAllowed, "platform_not_allowed"},
		{"jwt (double-wrapped)", fmt.Errorf("%w: %w", ErrJWTInvalid, errors.New("nonce")), "jwt_invalid"},
		{"unknown", errors.New("boom"), "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Reason(tt.err))
		})
	}
}

func TestErrorReasonsCoversMappedErrors(t *testing.T) {
	mapped := []error{
		ErrChallengeMismatch, ErrPubKeyMismatch, ErrChainIDMismatch, ErrMagicPassDisabled,
		ErrTokenTooOld, ErrSecBootDisabled, ErrDebugNotAllowed, ErrCodeHashNotAllowed,
		ErrPlatformNotAllowed, ErrJWTInvalid,
	}
	for _, err := range mapped {
		reason := Reason(err)
		require.NotEqual(t, "ok", reason, "a mapped error must not map to the nil-error reason")
		require.Contains(t, ErrorReasons, reason, "Reason(%v)=%q missing from ErrorReasons", err, reason)
	}

	require.Equal(t, "ok", Reason(nil))
	require.NotContains(t, ErrorReasons, "ok", `"ok" is the nil-error result, not an error reason`)

	require.Equal(t, "other", Reason(errors.New("x")))
	require.Contains(t, ErrorReasons, "other", "the default reason must be enumerated for pre-init")
}

func TestIsMagicPass(t *testing.T) {
	require.True(t, IsMagicPass(&types.TeeInfoResponse{Attestation: teeattestation.MagicPass}))
	require.False(t, IsMagicPass(&types.TeeInfoResponse{}))
}
