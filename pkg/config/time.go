package config

import (
	"errors"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/database"
)

type Timing struct {
	T0        uint64 `toml:"t0"`
	VoteEpoch uint64 `toml:"vote_epoch"` // duration of a VoteEpoch in seconds
}

type StorageTiming struct {
	CycleInternal          time.Duration `toml:"cycle_internal"`
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"`
}

// BlockToVotingEpochID returns voting epoch id for a block.
func (t *Timing) BlockToVotingEpochID(b database.Block) uint32 {
	return uint32(b.Timestamp - t.T0/t.VoteEpoch)
}

// validate checks that vote epoch has a positive duration.
func (t Timing) validate() error {
	if t.VoteEpoch < 1 {
		return errors.New("invalid vote epoch duration")
	}
	return nil
}

func (st StorageTiming) validate() error {
	if st.CycleInternal <= 0 || st.CycleQueueResponseWait <= 0 {
		return errors.New("invalid storage timing config")
	}
	return nil
}
