package liveness

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/service/info"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setup(t *testing.T, dbName string) (*gorm.DB, *miniredis.Miniredis, *redis.Client, *info.Service) {
	t.Helper()

	db, _ := testutil.InMemoryDB(t, dbName)
	err := db.AutoMigrate(&database.Block{})
	require.NoError(t, err)

	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	infoSvc := &info.Service{}

	return db, mr, redisClient, infoSvc
}

func TestNew(t *testing.T) {
	db, _, redisClient, infoSvc := setup(t, "new")

	l := New(db, redisClient, infoSvc, nil, nil)
	require.NotNil(t, l)
	require.False(t, l.startUpFinished)
}

func TestSignalStartupFinished(t *testing.T) {
	l := New(nil, nil, nil, nil, nil)
	require.False(t, l.startUpFinished)

	err := l.Startup(t.Context())
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrStartUpNotFinished))

	l.SignalStartupFinished()
	require.True(t, l.startUpFinished)

	err = l.Startup(t.Context())
	require.NoError(t, err)
}

func TestReady(t *testing.T) {
	t.Run("not started up", func(t *testing.T) {
		l := New(nil, nil, nil, nil, nil)
		err := l.Ready(t.Context())
		require.ErrorIs(t, err, ErrStartUpNotFinished)
	})

	t.Run("redis ping fails", func(t *testing.T) {
		db, mr, redisClient, infoSvc := setup(t, "ping")
		l := New(db, redisClient, infoSvc, nil, nil)
		l.SignalStartupFinished()

		mr.Close() // Close redis to make ping fail
		err := l.Ready(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "redis did not PONG")
	})

	t.Run("c-chain indexer delay", func(t *testing.T) {
		db, _, redisClient, infoSvc := setup(t, "delay")
		l := New(db, redisClient, infoSvc, nil, nil)
		l.SignalStartupFinished()

		bts := uint64(time.Now().Add(-15 * time.Minute).Unix())

		addState(t, db, bts)

		// No blocks in DB will cause a delay check failure
		err := l.Ready(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "c-chain indexer delay")
	})

	t.Run("info service not initialized", func(t *testing.T) {
		db, _, redisClient, _ := setup(t, "ok")

		l := New(db, redisClient, nil, nil, nil) // Pass nil for info service
		l.SignalStartupFinished()

		bts := uint64(time.Now().Unix())
		addState(t, db, bts)

		err := l.Ready(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "info service not initialized")
	})

	t.Run("info delay too high", func(t *testing.T) {
		db, _, redisClient, infoSvc := setup(t, "ok2")
		// Create a block to pass the db check
		block, _ := testutil.CreateBlock("0", 1)
		require.NoError(t, db.Create(block).Error)

		l := New(db, redisClient, infoSvc, nil, nil)
		l.SignalStartupFinished()

		bts := uint64(time.Now().Unix())
		addState(t, db, bts)

		// Set last updated to be outside the tolerance
		infoSvc.LastUpdated = time.Now().Add(-infoDelayTolerance * 2)

		err := l.Ready(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "no new info in last")
	})

	t.Run("all checks pass", func(t *testing.T) {
		db, _, redisClient, infoSvc := setup(t, "totally ok")

		l := New(db, redisClient, infoSvc, nil, nil)
		l.SignalStartupFinished()

		bts := uint64(time.Now().Unix())
		addState(t, db, bts)

		// Set last updated to be within the tolerance
		infoSvc.LastUpdated = time.Now()

		err := l.Ready(t.Context())
		require.NoError(t, err)
	})
}

func TestCChainDelayGauge(t *testing.T) {
	db, _, redisClient, infoSvc := setup(t, "cchain-gauge")
	m := metrics.New(metrics.Config{Enable: true, Liveness: true})

	addState(t, db, uint64(time.Now().Add(-15*time.Minute).Unix()))

	New(db, redisClient, infoSvc, nil, m)

	require.InDelta(t, (15 * time.Minute).Seconds(), cchainGaugeValue(t, m), 60, "delay reflects the indexer block timestamp")
}

func TestCChainDelayGaugeReadError(t *testing.T) {
	// No last_database_block row: FetchState fails, so the gauge must report 0 rather than
	// a bogus time.Since(epoch). This is the branch the critical TeeProxyCChainStale rests on.
	// NOTE: FetchState retries under the 2s timeout, so Gather blocks ~2s here — expected, not a hang.
	db, _, redisClient, infoSvc := setup(t, "cchain-read-error")
	require.NoError(t, db.AutoMigrate(&database.State{})) // table exists but is empty
	m := metrics.New(metrics.Config{Enable: true, Liveness: true})

	New(db, redisClient, infoSvc, nil, m)

	require.Zero(t, cchainGaugeValue(t, m), "a read error must report 0, not time.Since(epoch)")
}

func TestCChainDelayGaugeNotRegisteredWithoutDB(t *testing.T) {
	m := metrics.New(metrics.Config{Enable: true, Liveness: true})
	New(nil, nil, nil, nil, m) // db nil -> the c-chain gauge must not be registered

	_, found := cchainGauge(t, m)
	require.False(t, found)
}

// cchainGauge returns the c-chain delay gauge value and whether it is registered.
func cchainGauge(t *testing.T, m *metrics.Metrics) (float64, bool) {
	t.Helper()
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == "teeproxy_cchain_indexer_delay_seconds" {
			return f.GetMetric()[0].GetGauge().GetValue(), true
		}
	}
	return 0, false
}

// cchainGaugeValue returns the c-chain delay gauge value, failing if it is not registered.
func cchainGaugeValue(t *testing.T, m *metrics.Metrics) float64 {
	t.Helper()
	v, found := cchainGauge(t, m)
	require.True(t, found, "c-chain delay gauge must be registered")
	return v
}

func addState(t *testing.T, db *gorm.DB, blockTime uint64) {
	t.Helper()

	state := database.State{
		Name:           "last_database_block",
		Index:          12,
		BlockTimestamp: blockTime,
		Updated:        time.Now(),
	}

	err := db.AutoMigrate(&database.State{})
	require.NoError(t, err)

	db.Create(&state)
}
