package liveness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/tee-proxy/internal/service/info"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	cChainDelayTolerance = 140 * time.Second
	infoDelayTolerance   = 140 * time.Second
)

var ErrStartUpNotFinished = errors.New("startup not finished yet")

type liveness struct {
	startUpFinished bool

	db     *gorm.DB
	client *redis.Client
	info   *info.Service
}

func New(db *gorm.DB, client *redis.Client, info *info.Service) liveness {
	return liveness{
		startUpFinished: false,
		db:              db,
		client:          client,
		info:            info,
	}
}

// SignalStartupFinished sets startUpFinished to true indicating that the startup has finished.
func (l *liveness) SignalStartupFinished() {
	l.startUpFinished = true
}

func (l *liveness) Startup(_ context.Context) error {
	if !l.startUpFinished {
		return ErrStartUpNotFinished
	}

	return nil
}

func (l *liveness) Ready(ctx context.Context) error {
	if !l.startUpFinished {
		return ErrStartUpNotFinished
	}

	err := l.client.Ping(ctx).Err()
	if err != nil {
		return fmt.Errorf("redis did not PONG: %w", err)
	}

	err = database.CheckDelay(ctx, l.db, cChainDelayTolerance)
	if err != nil {
		return fmt.Errorf("c-chain indexer delay: %w", err)
	}

	l.info.RLock()
	delay := time.Since(l.info.LastUpdated)
	l.info.RUnlock()

	if delay > infoDelayTolerance {
		return fmt.Errorf("no new info in last %v", delay)
	}

	return nil
}
