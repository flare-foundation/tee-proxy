package liveness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/service/info"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/redis/go-redis/v9"
)

const (
	cChainDelayTolerance = 140 * time.Second
	infoDelayTolerance   = 140 * time.Second

	// cChainDelayReadTimeout bounds the scrape-time chain read for the delay gauge.
	cChainDelayReadTimeout = 2 * time.Second
)

var (
	// ErrStartUpNotFinished is returned by Startup and Ready before SignalStartupFinished is called.
	ErrStartUpNotFinished       = errors.New("startup not finished yet")
	errInfoServiceUninitialized = errors.New("info service not initialized")
)

// headSource reports the newest block the chain source holds.
type headSource interface {
	Head(ctx context.Context) (number, timestamp uint64, err error)
}

type liveness struct {
	startUpFinished bool

	chain   headSource
	client  *redis.Client
	info    *info.Service
	result  *result.Service
	metrics *metrics.Metrics

	sync.RWMutex
}

// New creates a liveness checker over the chain source, Redis client, and info/result services.
// m may be nil or disabled.
func New(chain headSource, client *redis.Client, info *info.Service, results *result.Service, m *metrics.Metrics) *liveness {
	l := &liveness{
		startUpFinished: false,
		chain:           chain,
		client:          client,
		info:            info,
		result:          results,
		metrics:         m,
	}

	if info != nil {
		m.RegisterInfoDelay(func() float64 {
			info.RLock()
			delay := time.Since(info.LastUpdated)
			info.RUnlock()
			return delay.Seconds()
		})
	}

	if chain != nil {
		// Scrape-time read of the source's last block timestamp. A read error reports 0
		// and consumes the full cChainDelayReadTimeout (reads retry under the ctx);
		// the source-down case is caught by readiness (TeeProxyNotReady), so this gauge targets
		// the stale-but-reachable source, which reads fine and returns a large delay.
		m.RegisterCChainDelay(func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), cChainDelayReadTimeout)
			defer cancel()
			_, timestamp, err := chain.Head(ctx)
			if err != nil {
				logger.Warnf("cchain delay gauge: reading chain head: %v", err)
				return 0
			}
			return time.Since(time.Unix(int64(timestamp), 0)).Seconds()
		})
	}

	return l
}

// SignalStartupFinished marks the proxy as past its bootstrap phase, flipping Startup probes to pass.
func (l *liveness) SignalStartupFinished() {
	l.Lock()
	defer l.Unlock()
	l.startUpFinished = true
}

// Startup signals that the proxy has finished bootstrapping (config loaded, services started).
// A nil return is the Kubernetes-style startup probe pass.
func (l *liveness) Startup(_ context.Context) error {
	l.RLock()
	defer l.RUnlock()

	if !l.startUpFinished {
		return ErrStartUpNotFinished
	}

	return nil
}

// Ready signals that the proxy is fit to serve traffic: startup is done, Redis answers,
// the c-chain indexer is current, info updates are fresh, and attestation and result
// storage are not in a known-failed state.
func (l *liveness) Ready(ctx context.Context) (err error) {
	defer func() { l.metrics.SetReady(err == nil) }()

	l.RLock()
	defer l.RUnlock()

	if !l.startUpFinished {
		return ErrStartUpNotFinished
	}

	err = l.client.Ping(ctx).Err()
	if err != nil {
		return fmt.Errorf("redis did not PONG: %w", err)
	}

	err = checkDelay(ctx, l.chain, cChainDelayTolerance)
	if err != nil {
		return fmt.Errorf("c-chain indexer delay: %w", err)
	}

	if l.info == nil {
		return errInfoServiceUninitialized
	}

	l.info.RLock()
	delay := time.Since(l.info.LastUpdated)
	l.info.RUnlock()

	if delay > infoDelayTolerance {
		return fmt.Errorf("no new info in last %v", delay)
	}

	if aErr := l.info.LastAttestationErr(); aErr != nil {
		return fmt.Errorf("attestation failing: %w", aErr)
	}

	if l.result != nil {
		if sErr := l.result.LastStorageErr(); sErr != nil {
			return fmt.Errorf("result storage failing: %w", sErr)
		}
	}

	return nil
}

// checkDelay fails when the chain source's newest block is older than tolerance.
func checkDelay(ctx context.Context, chain headSource, tolerance time.Duration) error {
	_, timestamp, err := chain.Head(ctx)
	if err != nil {
		return fmt.Errorf("fetching state: %w", err)
	}

	outOfSync := time.Since(time.Unix(int64(timestamp), 0))
	if outOfSync > tolerance {
		return fmt.Errorf("database out of sync for %v", outOfSync)
	}

	return nil
}
