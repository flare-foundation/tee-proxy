package utils

import (
	"crypto/ecdsa"
	"fmt"
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
	"io"
	"math/rand"
	"testing"
	"time"

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
	Interval: 50 * time.Millisecond,
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

// RunProxy simulates behaviour of internal/initialize.go - Starts internal and external proxy servers, and fetches TEE ID from TEE
func RunProxy(t *testing.T, internalPort, externalPort uint, proxyPk *ecdsa.PrivateKey) (*ProxyConfig, func()) {
	t.Helper()

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

	go func() {
		logger.Info("Starting internal server")
		err := internal.Serve()
		require.NoError(t, err)
	}()

	infoStorage := info.NewStorage(db, aq, rs, &config.StorageTiming{
		CycleInternal:          TestTimeConfig.Interval,
		CycleQueueResponseWait: TestTimeConfig.Timeout,
	})

	initialInfo, err := infoStorage.FetchInfo(t.Context())
	require.NoError(t, err)

	go func() {
		err := infoStorage.Run(t.Context())
		require.NoError(t, err)
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

	cleanup := func() {
		logger.Info("Flushing redis")
		c.FlushAll(t.Context())
		_ = c.Close()
		mr.Close()
	}

	instService := instructionService.NewService(teeId, proxyPk, make(chan policy.SigningPolicy, 1), aq, vs)
	external := server.NewExternal(fmt.Sprintf("%d", externalPort), &instService, &actionService, &resultService, &infoStorage, &walletStorage)

	go func() {
		err := instService.Forward(t.Context())
		require.NoError(t, err)
	}()

	go func() {
		logger.Info("Starting external server")
		err := external.Serve()
		require.NoError(t, err)
	}()

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

	for i := 0; i < n; i++ {
		// Generate random float between 0 and 1
		numbers[i] = r.Float64()
		sum += numbers[i]
	}

	// Normalize to sum to 1
	for i := 0; i < n; i++ {
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
