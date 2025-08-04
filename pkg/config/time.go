package config

import (
	"errors"
	"time"
)

type Timing struct {
	T0        uint64 `toml:"t0"`
	VoteEpoch uint64 `toml:"vote_epoch"` // duration of a VoteEpoch in seconds
}

type StorageTiming struct {
	CycleInternal          time.Duration `toml:"cycle_internal"`
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"`
}

// TimestampToVotingEpochID returns voting epoch id for timestamp.
func (t *Timing) TimestampToVotingEpochID(ts uint64) uint32 {
	return uint32((ts - t.T0) / t.VoteEpoch)
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
