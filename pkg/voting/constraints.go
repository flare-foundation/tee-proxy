package voting

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/op"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

type sizeConstraint struct {
	originalMessage           int
	additionalFixedMessage    int
	additionalVariableMessage int
}

var defaultConstraint = sizeConstraint{
	originalMessage:           50 * 1024,
	additionalFixedMessage:    100 * 1024,
	additionalVariableMessage: 50 * 1024,
}

var noAdditional = sizeConstraint{
	originalMessage:           50 * 1024,
	additionalFixedMessage:    0,
	additionalVariableMessage: 0,
}

var restore = sizeConstraint{
	originalMessage:           50 * 1024,
	additionalFixedMessage:    100 * 1024,
	additionalVariableMessage: 1024 * 1024,
}

func constraints(opCommand common.Hash) (sizeConstraint, error) {
	oc := op.HashToOPCommand(opCommand)

	switch oc {
	case op.InitializePolicy, op.UpdatePolicy, op.KeyInfo, op.TEEInfo, op.TEEBackup:
		return sizeConstraint{}, fmt.Errorf("%w: non instruction opCommand", status.HTTP[400])
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
		return fmt.Errorf("%w: original message to big", status.HTTP[400])
	case len(data.AdditionalFixedMessage) > c.additionalFixedMessage:
		return fmt.Errorf("%w: additional fixed message message to big", status.HTTP[400])
	case len(data.AdditionalVariableMessage) > c.additionalVariableMessage:
		return fmt.Errorf("%w: additional variable message message to big", status.HTTP[400])
	}

	return nil
}
