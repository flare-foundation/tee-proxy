package proxy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

// resolveGovernance converts the optional governance config into the
// types.Governance the machine-path service uses to pre-verify Safe approvals,
// after cross-checking it against the node's attested governance hash.
//
// When no governance is configured it returns the zero value, which disables
// Safe-approval pre-verification — the proxy then forwards direct governance
// signatures only (the legacy path) and nothing is cross-checked. When
// configured, it recomputes the governance hash from the configured signer set
// (plain or Safe flavor) and requires it to equal nodeGovernanceHash, so the
// proxy pre-verifies against exactly the governance the node enforces; a
// mismatch is a hard error.
func resolveGovernance(cfg config.Governance, nodeGovernanceHash common.Hash) (types.Governance, error) {
	if !cfg.IsSet() {
		return types.Governance{}, nil
	}

	gov := types.Governance{
		Signers:    cfg.Signers,
		Threshold:  cfg.Threshold,
		Safe:       cfg.Safe,
		TeeManager: cfg.TeeManager,
	}

	var expected common.Hash
	var err error
	if cfg.SafeBacked() {
		expected, err = types.GovernanceHashSafe(gov.TeeManager, gov.Safe, gov.Signers, gov.Threshold)
	} else {
		expected, err = types.GovernanceHash(gov.Signers, gov.Threshold)
	}
	if err != nil {
		return types.Governance{}, fmt.Errorf("computing governance hash: %w", err)
	}
	if expected != nodeGovernanceHash {
		return types.Governance{}, fmt.Errorf(
			"configured governance hash %s does not match the node's governance hash %s",
			expected, nodeGovernanceHash,
		)
	}
	gov.Hash = expected
	return gov, nil
}
