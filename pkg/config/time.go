package config

import (
	"errors"
	"time"
)

type StorageTiming struct {
	CycleInternal          time.Duration `toml:"cycle_internal"`
	CycleQueueResponseWait time.Duration `toml:"cycle_queue_response_wait"`
}

func (st StorageTiming) validate() error {
	if st.CycleInternal <= 0 || st.CycleQueueResponseWait <= 0 {
		return errors.New("invalid storage timing config")
	}
	return nil
}
