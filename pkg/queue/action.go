package queue

import (
	"crypto/rand"
	"encoding/json"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/tee-node/pkg/types"
)

// PrepareDirectAction prepares an action with direct instruction.
//
// With type set to "direct" and tag set to "submit".
// todo: find a better place.
func PrepareDirectAction(opType constants.OPType, opCommand constants.OPCommand, msg []byte) (*types.Action, error) {
	id, err := randID()
	if err != nil {
		return nil, err
	}

	di := types.DirectInstruction{
		Data: types.DirectInstructionData{
			OPType:    opType.Hash(),
			OPCommand: opCommand.Hash(),
			Message:   msg,
		},
		Signatures: nil,
	}

	dim, err := json.Marshal(di)
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
		Signatures:                 nil,
		AdditionalVariableMessages: nil,
		Timestamps:                 nil,
		AdditionalActionData:       nil,
	}, nil
}

// randID returns cryptographically random common.Hash.
func randID() (common.Hash, error) {
	x := common.Hash{}
	n, err := rand.Read(x[:])

	if err != nil {
		return x, err
	}
	if n != 32 {
		return x, errors.New("not enough random bytes")
	}

	return x, nil
}
