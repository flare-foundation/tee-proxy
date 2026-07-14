package attestation

import (
	"errors"

	teeattestation "github.com/flare-foundation/tee-node/pkg/attestation"
	"github.com/flare-foundation/tee-node/pkg/types"
)

// ErrorReasons lists every label Reason returns for a non-nil error.
// It is the closed error set that metrics pre-initialization enumerates.
var ErrorReasons = []string{"challenge_mismatch", "pubkey_mismatch", "chain_id_mismatch", "magic_pass_disabled", "token_too_old", "sec_boot_disabled", "debug_not_allowed", "code_hash_not_allowed", "platform_not_allowed", "jwt_invalid", "other"}

// ReasonMagicPass is the attestation_verify_total reason label callers emit for an accepted
// magic_pass sentinel (result "ok"). It is deliberately NOT returned by Reason, which
// classifies only errors — an accepted magic_pass yields a nil error.
const ReasonMagicPass = "magic_pass"

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
