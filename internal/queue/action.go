package queue

import (
	"encoding/json"

	"github.com/flare-foundation/go-flare-common/pkg/random"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/types"
)

// PrepareDirectAction wraps a payload as a Direct/Submit action ready for enqueue.
func PrepareDirectAction(opType op.Type, opCommand op.Command, msg []byte) (*types.Action, error) {
	di := &types.DirectInstruction{
		OPType:    opType.Hash(),
		OPCommand: opCommand.Hash(),
		Message:   msg,
	}

	return DirectInstructionToAction(di)
}

// DirectInstructionToAction converts a DirectInstruction into an Action with a randomly generated ID.
func DirectInstructionToAction(i *types.DirectInstruction) (*types.Action, error) {
	id, err := random.Hash()
	if err != nil {
		return nil, err
	}

	dim, err := json.Marshal(i)
	if err != nil {
		return nil, err
	}

	ad := types.ActionData{
		ID:   id,
		Type: types.Direct,

		SubmissionTag: types.Submit,
		Message:       dim,
	}

	return &types.Action{
		Data:                       ad,
		AdditionalVariableMessages: nil,
		AdditionalActionData:       nil,
		Signatures:                 nil,
		Timestamps:                 nil,
	}, nil
}
