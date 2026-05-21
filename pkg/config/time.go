package config

import (
	"errors"
	"time"
)

var errInvalidInfoTiming = errors.New("invalid info timing config")

type InfoTiming struct {
	Initial                time.Duration `toml:"initial_timeout"`
	CycleInternal          time.Duration `toml:"cycle_internal"`
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"`
}

func (st InfoTiming) validate() error {
	// Initial == 0 is documented as "no timeout"; only negative values are rejected.
	if st.Initial < 0 || st.CycleInternal <= 0 || st.CycleQueueResponseWait <= 0 {
		return errInvalidInfoTiming
	}
	return nil
}
