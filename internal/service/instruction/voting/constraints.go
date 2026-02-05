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
	restore = sizeConstraint{
		originalMessage:           50 * kib,
		additionalFixedMessage:    100 * kib,
		additionalVariableMessage: mib,
	}
)

var (
	errNonInstructionCommand    = fmt.Errorf("%w: non instruction opCommand", status.HTTP[400])
	errOriginalMessageTooBig    = fmt.Errorf("%w: original message to big", status.HTTP[400])
	errAdditionalMessageTooBig  = fmt.Errorf("%w: additional message to big", status.HTTP[400])
	errAdditionalVariableTooBig = fmt.Errorf("%w: additional variable message to big", status.HTTP[400])
)

// constraints returns instruction size constraints for opCommand.
func constraints(opCommand common.Hash) (sizeConstraint, error) {
	oc := op.HashToOPCommand(opCommand)

	switch oc {
	case op.InitializePolicy, op.UpdatePolicy, op.KeyInfo, op.TEEInfo, op.TEEBackup:
		return sizeConstraint{}, errNonInstructionCommand
	case op.KeyDataProviderRestore, op.KeyDataProviderRestoreTest:
		return restore, nil
	case op.Pay, op.Reissue, op.TEEAttestation, op.KeyGenerate, op.KeyDelete:
		return noAdditional, nil
	case op.Prove:
		return defaultConstraint, nil
	default:
		return defaultConstraint, nil
	}
}

func checkSize(data *instruction.Data) error {
	c, err := constraints(data.OPCommand)
	if err != nil {
		return err
	}

	switch {
	case len(data.OriginalMessage) > c.originalMessage:
		return errOriginalMessageTooBig
	case len(data.AdditionalFixedMessage) > c.additionalFixedMessage:
		return errAdditionalMessageTooBig
	case len(data.AdditionalVariableMessage) > c.additionalVariableMessage:
		return errAdditionalVariableTooBig
	}

	return nil
}
