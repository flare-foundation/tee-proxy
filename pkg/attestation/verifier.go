// Package attestation verifies the Google Confidential Space attestation token in TeeInfoResponse.
// Bedrock checks (JWT chain, nonce, challenge, pubkey consistency) always run when Enabled.
// Measurement checks run only when their allowlist or flag is set.
package attestation

import (
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/attestation/googlecloud"
	teeattestation "github.com/flare-foundation/tee-node/pkg/attestation"
	"github.com/flare-foundation/tee-node/pkg/types"
)

//go:embed root_certs/google_confidential_space_root.crt
var googleCSRootPEM []byte

// GoogleCSRoot returns the embedded Confidential Space root certificate.
func GoogleCSRoot() (*x509.Certificate, error) {
	block, _ := pem.Decode(googleCSRootPEM)
	if block == nil {
		return nil, errors.New("decoding embedded Google CS root: no PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing embedded Google CS root: %w", err)
	}

	return cert, nil
}

// Config controls Verify. When Enabled is false, Verify is a no-op.
// Each optional check runs only when its setting is non-zero.
type Config struct {
	Enabled  bool
	RootCert *x509.Certificate // required when Enabled

	// Optional CRLs; nil skips the corresponding check.
	LeafCRL         *x509.RevocationList
	IntermediateCRL *x509.RevocationList

	ExpectedCodeHash    []common.Hash // empty → skip
	ExpectedPlatform    []common.Hash // empty → skip
	ExpectedDebugStatus []string      // empty → skip
	MaxTokenAge         time.Duration // 0 → skip
	RequireSecBoot      bool          // false → skip

	// AllowMagicPass accepts the tee-node sentinel; dev/test only.
	AllowMagicPass bool
}

var (
	ErrDisabled          = errors.New("attestation disabled")
	ErrMagicPassDisabled = errors.New("magic_pass attestation not allowed")
	ErrChallengeMismatch = errors.New("attestation challenge does not match sent challenge")
	ErrPubKeyMismatch    = errors.New("TeeInfo and MachineData public keys differ")
	ErrNonceMismatch     = errors.New("attestation nonce does not bind to TeeInfo hash")
	ErrTokenTooOld       = errors.New("attestation token issued too long ago")
	ErrSecBootDisabled   = errors.New("attestation reports secure boot disabled")
	ErrDebugNotAllowed   = errors.New("attestation debug status not in allowlist")
	ErrCodeHashNotAllow  = errors.New("attestation code hash not in allowlist")
	ErrPlatformNotAllow  = errors.New("attestation platform not in allowlist")
)

// ActiveChecks reports which optional checks Verify will perform. Used for the startup log line.
type ActiveChecks struct {
	CodeHash    bool
	Platform    bool
	DebugStatus bool
	MaxTokenAge bool
	SecBoot     bool
	MagicPass   bool
}

func (cfg *Config) Active() ActiveChecks {
	return ActiveChecks{
		CodeHash:    len(cfg.ExpectedCodeHash) > 0,
		Platform:    len(cfg.ExpectedPlatform) > 0,
		DebugStatus: len(cfg.ExpectedDebugStatus) > 0,
		MaxTokenAge: cfg.MaxTokenAge > 0,
		SecBoot:     cfg.RequireSecBoot,
		MagicPass:   cfg.AllowMagicPass,
	}
}

// Verify validates the attestation in tir against cfg.
func Verify(tir *types.TeeInfoResponse, sentChallenge common.Hash, cfg *Config) error {
	if !cfg.Enabled {
		return nil
	}

	if err := verifyBedrock(tir, sentChallenge); err != nil {
		return err
	}

	if tir.Attestation == teeattestation.MagicPass {
		if !cfg.AllowMagicPass {
			return ErrMagicPassDisabled
		}
		return nil
	}

	claims := &googlecloud.GoogleTeeClaims{}
	_, _, err := googlecloud.ParseAndValidatePKITokenClaims(tir.Attestation, cfg.RootCert, cfg.LeafCRL, cfg.IntermediateCRL, claims)
	if err != nil {
		return fmt.Errorf("validating attestation JWT: %w", err)
	}

	teeInfoHash, err := tir.TeeInfo.Hash()
	if err != nil {
		return fmt.Errorf("hashing TeeInfo for nonce check: %w", err)
	}
	expectedNonce := hex.EncodeToString(teeInfoHash)
	if !slices.Contains([]string(claims.EATNonce), expectedNonce) {
		return ErrNonceMismatch
	}

	return verifyClaims(claims, cfg)
}

func verifyBedrock(tir *types.TeeInfoResponse, sentChallenge common.Hash) error {
	if tir.TeeInfo.Challenge != sentChallenge {
		return ErrChallengeMismatch
	}
	if tir.TeeInfo.PublicKey != tir.MachineData.PublicKey {
		return ErrPubKeyMismatch
	}
	return nil
}

func verifyClaims(claims *googlecloud.GoogleTeeClaims, cfg *Config) error {
	if cfg.MaxTokenAge > 0 {
		if claims.IssuedAt == nil {
			return fmt.Errorf("%w: iat claim missing", ErrTokenTooOld)
		}
		if age := time.Since(claims.IssuedAt.Time); age > cfg.MaxTokenAge {
			return fmt.Errorf("%w: %v old", ErrTokenTooOld, age)
		}
	}

	if cfg.RequireSecBoot && !claims.SecBoot {
		return ErrSecBootDisabled
	}

	if len(cfg.ExpectedDebugStatus) > 0 && !slices.Contains(cfg.ExpectedDebugStatus, claims.DebugStatus) {
		return fmt.Errorf("%w: got %q", ErrDebugNotAllowed, claims.DebugStatus)
	}

	if len(cfg.ExpectedCodeHash) > 0 {
		ch, err := claims.CodeHash()
		if err != nil {
			return fmt.Errorf("reading code hash from claims: %w", err)
		}
		if !slices.Contains(cfg.ExpectedCodeHash, ch) {
			return fmt.Errorf("%w: got %s", ErrCodeHashNotAllow, ch)
		}
	}

	if len(cfg.ExpectedPlatform) > 0 {
		p, err := claims.Platform()
		if err != nil {
			return fmt.Errorf("reading platform from claims: %w", err)
		}
		if !slices.Contains(cfg.ExpectedPlatform, p) {
			return fmt.Errorf("%w: got %s", ErrPlatformNotAllow, p)
		}
	}

	return nil
}
