package config

import (
	"errors"
	"time"
)

var errInvalidInfoTiming = errors.New("invalid info timing config")

// InfoTiming controls the bootstrap and steady-state cadence of TEE info refreshes.
type InfoTiming struct {
	Initial                time.Duration `toml:"initial_timeout"`           // Bound on the bootstrap fetch; 0 means no timeout.
	CycleInternal          time.Duration `toml:"cycle_internal"`            // Period of the internal refresh cycle.
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"` // Per-refresh wait for a response.
}

func (st InfoTiming) validate() error {
	// Initial == 0 is documented as "no timeout"; only negative values are rejected.
	if st.Initial < 0 || st.CycleInternal <= 0 || st.CycleQueueResponseWait <= 0 {
		return errInvalidInfoTiming
	}
	return nil
}
