package config

import (
	"errors"

	"github.com/flare-foundation/go-flare-common/pkg/database"
)

type Timing struct {
	T0        uint64 `toml:"t0"`
	VoteEpoch uint64 `toml:"voteEpoch"` // duration of a VoteEpoch in seconds
}

// BlockToVotingEpochID returns voting epoch id for a block.
func (t *Timing) BlockToVotingEpochID(b database.Block) uint32 {
	return uint32(b.Timestamp - t.T0/t.VoteEpoch)
}

// validate checks that vote epoch has a positive duration.
func (a Timing) validate() error {
	if a.VoteEpoch < 1 {
		return errors.New("invalid vote epoch duration")
	}
	return nil
}
