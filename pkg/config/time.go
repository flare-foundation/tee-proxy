package config

import (
	"errors"
	"time"
)

type InfoTiming struct {
	Initial                time.Duration `toml:"initial_timeout"`
	CycleInternal          time.Duration `toml:"cycle_internal"`
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"`
}

func (st InfoTiming) validate() error {
	if st.CycleInternal <= 0 || st.CycleQueueResponseWait <= 0 {
		return errors.New("invalid info timing config")
	}
	return nil
}
