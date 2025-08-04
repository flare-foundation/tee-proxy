package utils

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/service/action"
	instructionService "github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/service/server"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/info"
	"github.com/flare-foundation/tee-proxy/pkg/meta"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"

	cryptorand "crypto/rand"
)

type ProxyConfig struct {
	ExtPort     uint
	IntPort     uint
	TeeId       common.Address
	ProxyPubKey *ecdsa.PublicKey
	Aq          *queue.ActionQueues
	Rs          *queue.ResponseStorage
	Vc          *voting.Config
	Vs          *voting.Storage
	Ws          *wallets.Storage
}

var TestTimeConfig = struct {
	Timeout  time.Duration
	Interval time.Duration
}{
	Timeout:  2000 * time.Millisecond,
	Interval: 5 * time.Millisecond,
}

func mockDB(t *testing.T) *gorm.DB {
	db, _ := testutil.InMemoryDB(t, "choose")
	err := db.AutoMigrate(&database.Block{})
	require.NoError(t, err)

	for i := uint64(1); i <= 3; i++ {
		block, _ := testutil.CreateBlock(fmt.Sprintf("%d", i), i)
		if err := db.Create(block).Error; err != nil {
			require.NoError(t, err)
		}
	}

	return db
}

// RunProxy simulates behavior of internal/initialize.go - Starts internal and external proxy servers, and fetches TEE ID from TEE
func RunProxy(t *testing.T, internalPort, externalPort uint, proxyPk *ecdsa.PrivateKey, wg *sync.WaitGroup) (*ProxyConfig, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	mr := miniredis.RunT(t)
	db := mockDB(t)

	c := queue.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c)
	rs := queue.NewResultStorage(c)

	// Setup action and result services
	walletStorage := wallets.NewStorage(aq, rs)
	actionService := action.NewService(aq)
	resultService := result.NewService(rs)

	internal := server.NewInternal(fmt.Sprintf("%d", internalPort), &actionService, &resultService, &walletStorage)

	wg.Add(1)
	go func() {
		logger.Info("Starting internal server")
		err := internal.Serve()
		require.Error(t, err)
		wg.Done()
	}()

	infoStorage := info.NewStorage(db, aq, rs, &config.StorageTiming{
		CycleInternal:          1 * time.Minute,
		CycleQueueResponseWait: TestTimeConfig.Timeout,
	})

	initialInfo, err := infoStorage.FetchInfo(t.Context())
	require.NoError(t, err)

	wg.Add(1)
	go func() {
		err := infoStorage.Run(ctx)
		require.Error(t, err)
		wg.Done()
	}()

	teePub, err := types.ParsePubKey(initialInfo.TeeInfo.PublicKey)
	require.NoError(t, err)
	teeId := crypto.PubkeyToAddress(*teePub)
	err = resultService.SetIdentity(teeId)
	require.NoError(t, err)

	metaObj := meta.New(&walletStorage)

	vc := &voting.Config{
		ProposalExpiration: 2 * time.Second,
		MaxPendingRequests: 100,
	}

	vs := voting.NewStorage(vc, 3, metaObj, 3)

	instService := instructionService.NewService(teeId, proxyPk, make(chan policy.SigningPolicy, 1), aq, vs)
	external := server.NewExternal(fmt.Sprintf("%d", externalPort), &instService, &actionService, &resultService, &infoStorage, &walletStorage)

	wg.Add(1)
	go func() {
		err := instService.Forward(ctx)
		require.Error(t, err)
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		logger.Info("Starting external server")
		err := external.Serve()
		require.Error(t, err)
		wg.Done()
	}()

	cleanup := func() {
		internal.Close()
		external.Close()
		cancel()
		logger.Info("Flushing redis")
		c.FlushAll(t.Context())
		_ = c.Close()
		mr.Close()
	}

	return &ProxyConfig{
		ExtPort:     externalPort,
		IntPort:     internalPort,
		TeeId:       teeId,
		ProxyPubKey: teePub,
		Aq:          aq,
		Rs:          rs,
		Vc:          vc,
		Vs:          vs,
		Ws:          &walletStorage,
	}, cleanup
}

// RandomNormalizedArray generates an array of n random floats that sum to 1
func RandomNormalizedArray(n int, seed int64) []float64 {
	// Initialize random source with seed
	source := rand.NewSource(seed)
	r := rand.New(source)

	// Generate random numbers
	numbers := make([]float64, n)
	sum := 0.0

	for i := range n {
		// Generate random float between 0 and 1
		numbers[i] = r.Float64()
		sum += numbers[i]
	}

	// Normalize to sum to 1
	for i := range n {
		numbers[i] /= sum
	}

	return numbers
}

func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(cryptorand.Reader, b); err != nil {
		return nil, err
	}

	return b, nil
}
