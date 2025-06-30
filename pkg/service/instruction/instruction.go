package instruction

import (
	"context"

	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
)

type Service struct {
	*voting.Manager
}

func (s *Service) ServeInstruction(_ context.Context, i *instruction.Instruction) (*voting.Receipt, error) {
	// err := requests.ValidateDataSize(&i.Data)
	// if err != nil {
	// 	return nil, err
	// }

	// err = requests.CheckRequest()

	return s.Process(i)
}
