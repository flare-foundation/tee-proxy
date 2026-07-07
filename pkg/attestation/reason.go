package attestation

import (
	"errors"

	teeattestation "github.com/flare-foundation/tee-node/pkg/attestation"
	"github.com/flare-foundation/tee-node/pkg/types"
)

// Reason maps a Verify error to a bounded label for metrics, so callers never
// derive labels from raw error text (which embeds token ages, hashes, addresses).
// A nil error yields "ok"; an unrecognized error yields "other".
func Reason(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrChallengeMismatch):
		return "challenge_mismatch"
	case errors.Is(err, ErrPubKeyMismatch):
		return "pubkey_mismatch"
	case errors.Is(err, ErrChainIDMismatch):
		return "chain_id_mismatch"
	case errors.Is(err, ErrMagicPassDisabled):
		return "magic_pass_disabled"
	case errors.Is(err, ErrTokenTooOld):
		return "token_too_old"
	case errors.Is(err, ErrSecBootDisabled):
		return "sec_boot_disabled"
	case errors.Is(err, ErrDebugNotAllowed):
		return "debug_not_allowed"
	case errors.Is(err, ErrCodeHashNotAllowed):
		return "code_hash_not_allowed"
	case errors.Is(err, ErrPlatformNotAllowed):
		return "platform_not_allowed"
	case errors.Is(err, ErrJWTInvalid):
		return "jwt_invalid"
	default:
		return "other"
	}
}

// IsMagicPass reports whether the response carries the tee-node magic_pass sentinel
// in place of a real attestation token.
func IsMagicPass(tir *types.TeeInfoResponse) bool {
	return tir.Attestation == teeattestation.MagicPass
}
