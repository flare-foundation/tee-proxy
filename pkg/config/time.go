package config

import (
	"errors"

	"github.com/flare-foundation/go-flare-common/pkg/database"
)

type Timing struct {
	T0        uint64
	VoteEpoch uint64
}

func (t *Timing) BlockToVotingRoundID(b database.Block) uint32 {
	return uint32(b.Timestamp - t.T0/t.VoteEpoch)
}

func (a Timing) validate() error {
	if a.VoteEpoch < 1 {
		return errors.New("invalid vote epoch duration")
	}
	return nil
}
