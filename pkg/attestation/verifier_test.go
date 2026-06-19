package attestation

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/tee/attestation/googlecloud"
	teeattestation "github.com/flare-foundation/tee-node/pkg/attestation"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const (
	testHWModel     = "test_model"
	testImageDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testAudience    = "test-audience"
)

// testImageIDHash is the common.Hash form of testImageDigest, suitable for
// googlecloud.Policy.AllowedImageIDs.
var testImageIDHash = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

// newTir builds a minimally valid TeeInfoResponse with matching pubkeys and the given challenge.
func newTir(t *testing.T, challenge common.Hash) *types.TeeInfoResponse {
	t.Helper()
	pk := types.PublicKey{
		X: common.HexToHash("0xaa"),
		Y: common.HexToHash("0xbb"),
	}
	return &types.TeeInfoResponse{
		TeeInfo: types.TeeInfo{
			Challenge:    challenge,
			PublicKey:    pk,
			TeeTimestamp: uint64(time.Now().Unix()),
		},
		MachineData: types.MachineData{PublicKey: pk},
	}
}

func TestVerifyDisabled(t *testing.T) {
	// Garbage response should still pass when Enabled is false.
	tir := newTir(t, common.HexToHash("0x01"))
	tir.MachineData.PublicKey = types.PublicKey{}
	require.NoError(t, Verify(tir, common.HexToHash("0x99"), &Config{Enabled: false}))
}

func TestVerifyChallengeMismatch(t *testing.T) {
	tir := newTir(t, common.HexToHash("0x01"))
	tir.Attestation = teeattestation.MagicPass
	err := Verify(tir, common.HexToHash("0x02"), &Config{Enabled: true, AllowMagicPass: true})
	require.ErrorIs(t, err, ErrChallengeMismatch)
}

func TestVerifyPubKeyMismatch(t *testing.T) {
	tir := newTir(t, common.HexToHash("0x01"))
	tir.MachineData.PublicKey = types.PublicKey{X: common.HexToHash("0xcc"), Y: common.HexToHash("0xdd")}
	tir.Attestation = teeattestation.MagicPass
	err := Verify(tir, common.HexToHash("0x01"), &Config{Enabled: true, AllowMagicPass: true})
	require.ErrorIs(t, err, ErrPubKeyMismatch)
}

func TestVerifyMagicPass(t *testing.T) {
	challenge := common.HexToHash("0x01")
	tir := newTir(t, challenge)
	tir.Attestation = teeattestation.MagicPass

	t.Run("disallowed", func(t *testing.T) {
		err := Verify(tir, challenge, &Config{Enabled: true, AllowMagicPass: false})
		require.ErrorIs(t, err, ErrMagicPassDisabled)
	})

	t.Run("allowed", func(t *testing.T) {
		require.NoError(t, Verify(tir, challenge, &Config{Enabled: true, AllowMagicPass: true}))
	})
}

func TestVerifyEmbeddedRootCertDecodes(t *testing.T) {
	cert, err := GoogleCSRoot()
	require.NoError(t, err)
	require.NotNil(t, cert)
	require.Equal(t, "Confidential Space Root CA", cert.Subject.CommonName)
}

func TestActiveReflectsConfig(t *testing.T) {
	cfg := &Config{
		Enabled:             true,
		Audience:            "rp",
		ExpectedCodeHash:    []common.Hash{common.HexToHash("0x1")},
		MaxTokenAge:         5 * time.Minute,
		RequireSecBoot:      true,
		ExpectedDebugStatus: []string{"disabled-since-boot"},
	}
	a := cfg.Active()
	require.True(t, a.Audience)
	require.True(t, a.CodeHash)
	require.False(t, a.Platform)
	require.True(t, a.DebugStatus)
	require.True(t, a.MaxTokenAge)
	require.True(t, a.SecBoot)
	require.False(t, a.MagicPass)
}

// TestVerifyJWT amortises one signed JWT across all claim-gated assertions.
func TestVerifyJWT(t *testing.T) {
	challenge := common.HexToHash("0xfeed")
	tir := newTir(t, challenge)

	teeInfoHash, err := tir.TeeInfo.Hash()
	require.NoError(t, err)
	nonce := hex.EncodeToString(teeInfoHash)

	codeHash, err := (&googlecloud.GoogleTeeClaims{
		SubMods: googlecloud.SubMods{Container: googlecloud.Container{ImageID: testImageDigest}},
	}).CodeHash()
	require.NoError(t, err)
	platformHash, err := convert.StringToCommonHash(testHWModel)
	require.NoError(t, err)

	root, signedJWT := buildTestJWT(t, googlecloud.GoogleTeeClaims{
		SecBoot:     true,
		HWModel:     testHWModel,
		DebugStatus: "disabled-since-boot",
		EATNonce:    googlecloud.EATNonce{nonce},
		SubMods:     googlecloud.SubMods{Container: googlecloud.Container{ImageID: testImageDigest}},
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tir.Attestation = signedJWT

	base := func() *Config {
		return &Config{
			Enabled:         true,
			RootCert:        root,
			Audience:        testAudience,
			AllowedImageIDs: []common.Hash{testImageIDHash},
		}
	}

	t.Run("happy path, no measurement checks", func(t *testing.T) {
		require.NoError(t, Verify(tir, challenge, base()))
	})

	t.Run("wrong root rejects", func(t *testing.T) {
		other, _ := generateTestCert(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), true, nil, nil)
		cfg := base()
		cfg.RootCert = other
		require.Error(t, Verify(tir, challenge, cfg))
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		bad := newTir(t, challenge)
		bad.TeeInfo.TeeTimestamp = tir.TeeInfo.TeeTimestamp + 1 // changes hash → nonce no longer matches
		bad.Attestation = tir.Attestation
		// The EAT nonce is enforced inside the library policy, so a mismatch
		// surfaces as the policy's eat_nonce error.
		err := Verify(bad, challenge, base())
		require.Error(t, err)
		require.ErrorContains(t, err, "nonce")
	})

	t.Run("code hash allowlist", func(t *testing.T) {
		cfg := base()
		cfg.ExpectedCodeHash = []common.Hash{codeHash}
		require.NoError(t, Verify(tir, challenge, cfg))

		cfg.ExpectedCodeHash = []common.Hash{common.HexToHash("0xdead")}
		require.ErrorIs(t, Verify(tir, challenge, cfg), ErrCodeHashNotAllowed)
	})

	t.Run("platform allowlist", func(t *testing.T) {
		cfg := base()
		cfg.ExpectedPlatform = []common.Hash{platformHash}
		require.NoError(t, Verify(tir, challenge, cfg))

		cfg.ExpectedPlatform = []common.Hash{common.HexToHash("0xbeef")}
		require.ErrorIs(t, Verify(tir, challenge, cfg), ErrPlatformNotAllowed)
	})

	t.Run("debug status allowlist", func(t *testing.T) {
		cfg := base()
		cfg.ExpectedDebugStatus = []string{"disabled-since-boot"}
		require.NoError(t, Verify(tir, challenge, cfg))

		cfg.ExpectedDebugStatus = []string{"never-this"}
		require.ErrorIs(t, Verify(tir, challenge, cfg), ErrDebugNotAllowed)
	})

	t.Run("sec boot gate", func(t *testing.T) {
		cfg := base()
		cfg.RequireSecBoot = true
		require.NoError(t, Verify(tir, challenge, cfg))
	})

	t.Run("max token age", func(t *testing.T) {
		cfg := base()
		cfg.MaxTokenAge = time.Hour
		require.NoError(t, Verify(tir, challenge, cfg))

		cfg.MaxTokenAge = time.Nanosecond
		time.Sleep(2 * time.Millisecond)
		err := Verify(tir, challenge, cfg)
		require.ErrorIs(t, err, ErrTokenTooOld)
	})
}

func TestVerifyMaxTokenAgeMissingIat(t *testing.T) {
	challenge := common.HexToHash("0xbeef")
	tir := newTir(t, challenge)
	teeInfoHash, err := tir.TeeInfo.Hash()
	require.NoError(t, err)
	nonce := hex.EncodeToString(teeInfoHash)

	root, signedJWT := buildTestJWT(t, googlecloud.GoogleTeeClaims{
		EATNonce: googlecloud.EATNonce{nonce},
		SubMods:  googlecloud.SubMods{Container: googlecloud.Container{ImageID: testImageDigest}},
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tir.Attestation = signedJWT

	err = Verify(tir, challenge, &Config{Enabled: true, RootCert: root, Audience: testAudience, AllowedImageIDs: []common.Hash{testImageIDHash}, MaxTokenAge: time.Hour})
	require.ErrorIs(t, err, ErrTokenTooOld)
}

func TestVerifyMaxTokenAgeFutureIat(t *testing.T) {
	challenge := common.HexToHash("0xf00d")
	tir := newTir(t, challenge)
	teeInfoHash, err := tir.TeeInfo.Hash()
	require.NoError(t, err)
	nonce := hex.EncodeToString(teeInfoHash)

	root, signedJWT := buildTestJWT(t, googlecloud.GoogleTeeClaims{
		EATNonce: googlecloud.EATNonce{nonce},
		SubMods:  googlecloud.SubMods{Container: googlecloud.Container{ImageID: testImageDigest}},
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Hour)),
		},
	})
	tir.Attestation = signedJWT

	err = Verify(tir, challenge, &Config{Enabled: true, RootCert: root, Audience: testAudience, AllowedImageIDs: []common.Hash{testImageIDHash}, MaxTokenAge: time.Hour})
	require.ErrorIs(t, err, ErrTokenTooOld)
}

func TestVerifySecBootRejectsWhenDisabled(t *testing.T) {
	challenge := common.HexToHash("0xcafe")
	tir := newTir(t, challenge)
	teeInfoHash, err := tir.TeeInfo.Hash()
	require.NoError(t, err)
	nonce := hex.EncodeToString(teeInfoHash)

	root, signedJWT := buildTestJWT(t, googlecloud.GoogleTeeClaims{
		SecBoot:  false,
		HWModel:  testHWModel,
		EATNonce: googlecloud.EATNonce{nonce},
		SubMods:  googlecloud.SubMods{Container: googlecloud.Container{ImageID: testImageDigest}},
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tir.Attestation = signedJWT

	err = Verify(tir, challenge, &Config{Enabled: true, RootCert: root, Audience: testAudience, AllowedImageIDs: []common.Hash{testImageIDHash}, RequireSecBoot: true})
	require.ErrorIs(t, err, ErrSecBootDisabled)
}

func TestVerifyAudience(t *testing.T) {
	const aud = "https://relying-party.example"
	challenge := common.HexToHash("0xa11d")
	tir := newTir(t, challenge)
	teeInfoHash, err := tir.TeeInfo.Hash()
	require.NoError(t, err)
	nonce := hex.EncodeToString(teeInfoHash)

	root, signedJWT := buildTestJWT(t, googlecloud.GoogleTeeClaims{
		EATNonce: googlecloud.EATNonce{nonce},
		SubMods:  googlecloud.SubMods{Container: googlecloud.Container{ImageID: testImageDigest}},
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{aud},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tir.Attestation = signedJWT

	t.Run("empty audience skips check", func(t *testing.T) {
		require.NoError(t, Verify(tir, challenge, &Config{Enabled: true, RootCert: root, AllowedImageIDs: []common.Hash{testImageIDHash}}))
	})

	t.Run("matching audience passes", func(t *testing.T) {
		require.NoError(t, Verify(tir, challenge, &Config{Enabled: true, RootCert: root, Audience: aud, AllowedImageIDs: []common.Hash{testImageIDHash}}))
	})

	t.Run("wrong audience rejected", func(t *testing.T) {
		err := Verify(tir, challenge, &Config{Enabled: true, RootCert: root, Audience: "someone-else", AllowedImageIDs: []common.Hash{testImageIDHash}})
		require.Error(t, err)
	})
}

func TestVerifyMalformedJWT(t *testing.T) {
	challenge := common.HexToHash("0xabcd")
	tir := newTir(t, challenge)
	tir.Attestation = "not.a.jwt"

	root, _ := generateTestCert(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), true, nil, nil)
	err := Verify(tir, challenge, &Config{Enabled: true, RootCert: root, Audience: testAudience, AllowedImageIDs: []common.Hash{testImageIDHash}})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrMagicPassDisabled))
}

func buildTestJWT(t *testing.T, claims googlecloud.GoogleTeeClaims) (*x509.Certificate, string) {
	t.Helper()
	// Verify pins the issuer to ConfidentialSpaceIssuer, which the library always enforces;
	// real Confidential Space tokens carry it. Default it so tests need not repeat it.
	if claims.Issuer == "" {
		claims.Issuer = googlecloud.ConfidentialSpaceIssuer
	}
	now := time.Now()
	root, rootKey := generateTestCert(t, now.Add(-time.Hour), now.Add(time.Hour), true, nil, nil)
	inter, interKey := generateTestCert(t, now.Add(-time.Hour), now.Add(time.Hour), true, root, rootKey)
	leaf, leafKey := generateTestCert(t, now.Add(-time.Hour), now.Add(time.Hour), false, inter, interKey)

	if len(claims.Audience) == 0 {
		claims.Audience = jwt.ClaimStrings{testAudience}
	}
	if claims.Issuer == "" {
		claims.Issuer = googlecloud.ConfidentialSpaceIssuer
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["x5c"] = []string{certToB64(leaf), certToB64(inter), certToB64(root)}

	signed, err := token.SignedString(leafKey)
	require.NoError(t, err)
	return root, signed
}

func generateTestCert(t *testing.T, notBefore, notAfter time.Time, isCA bool, parent *x509.Certificate, parentKey crypto.Signer) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		SignatureAlgorithm:    x509.SHA256WithRSA,
		PublicKeyAlgorithm:    x509.RSA,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	} else {
		template.KeyUsage = x509.KeyUsageDigitalSignature
	}

	if parent == nil {
		parent = template
		parentKey = priv
	}

	der, err := x509.CreateCertificate(rand.Reader, template, parent, &priv.PublicKey, parentKey)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, priv
}

func certToB64(c *x509.Certificate) string {
	return base64.StdEncoding.EncodeToString(c.Raw)
}
