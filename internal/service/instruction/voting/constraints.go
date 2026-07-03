package voting

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-proxy/pkg/status"
)

const (
	kib = 1 << 10
	mib = 1 << 20

	// maxTotalVariableMessage is the maximum total size of all aggregated variable messages in an action.
	// This should match the tee-node MaxVariableMessageSize setting.
	maxTotalVariableMessage = mib

	// maxCosigners caps the cosigner list length. Real admin/cosigner sets are single
	// digits; this only rejects abusive payloads that would inflate the cosigner map
	// allocation and the vote-hash ABI encode.
	maxCosigners = 1024
)

type sizeConstraint struct {
	originalMessage           int
	additionalFixedMessage    int
	additionalVariableMessage int
}

var (
	defaultConstraint = sizeConstraint{
		originalMessage:           50 * kib,
		additionalFixedMessage:    100 * kib,
		additionalVariableMessage: 50 * kib,
	}
	noAdditional = sizeConstraint{
		originalMessage:           50 * kib,
		additionalFixedMessage:    0,
		additionalVariableMessage: 0,
	}
)

var (
	errNonInstructionCommand    = fmt.Errorf("%w: non instruction opCommand", status.HTTP[400])
	errTooManyCosigners         = fmt.Errorf("%w: too many cosigners", status.HTTP[400])
	errOriginalMessageTooBig    = fmt.Errorf("%w: original message too big", status.HTTP[400])
	errAdditionalMessageTooBig  = fmt.Errorf("%w: additional message too big", status.HTTP[400])
	errAdditionalVariableTooBig = fmt.Errorf("%w: additional variable message too big", status.HTTP[400])
)

// restoreConstraint returns size constraints for restore operations.
// maxProviderVote is the maximal fraction of total weight a single provider can hold.
// If maxProviderVote is 0 (unset), the per-vote variable message limit defaults to maxTotalVariableMessage.
func restoreConstraint(maxProviderVote float64) sizeConstraint {
	variableLimit := maxTotalVariableMessage
	if maxProviderVote > 0 {
		variableLimit = int(float64(maxTotalVariableMessage) * maxProviderVote)
		if variableLimit == 0 {
			variableLimit = 1
		}
	}

	return sizeConstraint{
		originalMessage:           50 * kib,
		additionalFixedMessage:    100 * kib,
		additionalVariableMessage: variableLimit,
	}
}

// constraints returns instruction size constraints for opCommand.
// maxProviderVote is used to cap the per-vote variable message size for restore operations.
func constraints(opCommand common.Hash, maxProviderVote float64) (sizeConstraint, error) {
	oc := op.HashToOPCommand(opCommand)

	switch oc {
	case op.InitializePolicy, op.UpdatePolicy, op.KeyInfo, op.TEEInfo, op.TEEBackup:
		return sizeConstraint{}, errNonInstructionCommand
	case op.KeyDataProviderRestore, op.KeyDataProviderRestoreTest:
		return restoreConstraint(maxProviderVote), nil
	case op.Pay, op.Reissue, op.TEEAttestation, op.KeyGenerate, op.KeyDelete:
		return noAdditional, nil
	case op.Prove:
		return defaultConstraint, nil
	default:
		return defaultConstraint, nil
	}
}

// checkSize validates the instruction message sizes against the constraints.
func checkSize(data *instruction.Data, maxProviderVote float64) error {
	c, err := constraints(data.OPCommand, maxProviderVote)
	if err != nil {
		return err
	}

	switch {
	case len(data.Cosigners) > maxCosigners:
		return errTooManyCosigners
	case len(data.OriginalMessage) > c.originalMessage:
		return errOriginalMessageTooBig
	case len(data.AdditionalFixedMessage) > c.additionalFixedMessage:
		return errAdditionalMessageTooBig
	case len(data.AdditionalVariableMessage) > c.additionalVariableMessage:
		return errAdditionalVariableTooBig
	}

	return nil
}
