package config

import (
	"errors"
	"time"
)

var errInvalidInfoTiming = errors.New("invalid info timing config")

// InfoTiming controls the bootstrap and steady-state cadence of TEE info refreshes.
type InfoTiming struct {
	Initial                time.Duration `toml:"initial_timeout"`           // Budget for the whole bootstrap fetch, retries included; 0 means retry until it succeeds.
	CycleInternal          time.Duration `toml:"cycle_internal"`            // Period of the internal refresh cycle.
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"` // Per-attempt wait for a response.
	MaxAttempts            int           `toml:"max_attempts"`              // Attempts per steady-state refresh; the node answers a slow attestation with a failing response, so one attempt is not conclusive.
	RetryDelay             time.Duration `toml:"retry_delay"`               // Pause between attempts.
}

func (st InfoTiming) validate() error {
	// Initial == 0 is documented as "no timeout"; only negative values are rejected.
	if st.Initial < 0 || st.CycleInternal <= 0 || st.CycleQueueResponseWait <= 0 {
		return errInvalidInfoTiming
	}
	// a non-positive delay would let the unbounded bootstrap retry loop hammer the node
	if st.MaxAttempts < 1 || st.RetryDelay <= 0 {
		return errInvalidInfoTiming
	}
	return nil
}
