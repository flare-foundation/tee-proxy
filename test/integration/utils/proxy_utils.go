package utils

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	teewallets "github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/policy"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/liveness"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/server"
	"github.com/flare-foundation/tee-proxy/internal/service/info"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/internal/testutil"
	"github.com/flare-foundation/tee-proxy/pkg/attestation"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/voting"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	pkgwallets "github.com/flare-foundation/tee-proxy/pkg/wallets"
)

type ProxyConfig struct {
	ExtPort     uint
	IntPort     uint
	TeeID       common.Address
	TeePubKey   *ecdsa.PublicKey
	ProxyPubKey *ecdsa.PublicKey
	Aq          *queue.ActionQueues
	Rs          *result.ResultStorage
	Vc          *config.Voting
	Pc          chan policy.SigningPolicy
	Ws          *wallets.Service
	If          *info.Service
}

var TestTimeConfig = struct {
	Timeout  time.Duration
	Interval time.Duration
}{
	Timeout:  6000 * time.Millisecond,
	Interval: 50 * time.Millisecond,
}

var StorageTimeConfig = struct {
	CycleInternal          time.Duration
	CycleQueueResponseWait time.Duration
}{

	CycleInternal:          50 * time.Millisecond,
	CycleQueueResponseWait: 10 * time.Second,
}

func mockDB(t *testing.T) *gorm.DB {
	t.Helper()

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

// RunProxy simulates behavior of internal/proxy/proxy.go - Starts internal and external proxy servers, and fetches TEE ID from TEE
func RunProxy(t *testing.T, internalPort, externalPort uint, proxyPk *ecdsa.PrivateKey, wg *sync.WaitGroup) (*ProxyConfig, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	mr := miniredis.RunT(t)
	db := mockDB(t)

	c := storage.NewClient(mr.Addr())
	storageCfg := config.Storage{}
	storageCfg.SetDefault()
	aq := queue.NewActionQueues(c, storageCfg.ActionTTL, nil)
	rs := result.NewStorage(testutil.NewMemStorage[*types.ActionResponse](), storage.NewNotifier(c), storageCfg.ResultTTL, storageCfg.SubmitResultTTL)

	// Setup action and result services
	backupStore := testutil.NewMemStorage[*teewallets.TEEBackupResponse]()
	backupIndex := testutil.NewMemStorage[common.Hash]()
	resultService := result.NewService(rs, TestChainID, nil)
	walletStorage := wallets.NewService(aq, rs, backupIndex, backupStore, storageCfg.BackupTTL, nil)

	infoService := info.NewService(db, aq, rs, &config.InfoTiming{
		CycleInternal:          StorageTimeConfig.CycleInternal,
		CycleQueueResponseWait: StorageTimeConfig.CycleQueueResponseWait,
	}, &attestation.Config{Enabled: false}, nil)

	livenessService := liveness.New(db, c, infoService, resultService, nil)

	internal := server.NewInternal(fmt.Sprintf("%d", internalPort), aq, resultService, walletStorage, livenessService, nil)

	wg.Go(func() {
		logger.Info("Starting internal server")
		err := internal.Serve()
		require.Error(t, err)
	})

	initialInfo, sentChallenge, err := infoService.FetchInfo(t.Context(), 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, sentChallenge, initialInfo.TeeInfo.Challenge, "TEE info challenge round-trip mismatch")

	wg.Go(func() {
		err := infoService.Run(ctx)
		require.Error(t, err)
	})

	teePub, err := types.ParsePubKey(initialInfo.TeeInfo.PublicKey)
	require.NoError(t, err)
	teeID := crypto.PubkeyToAddress(*teePub)
	err = resultService.SetIdentity(teeID)
	require.NoError(t, err)

	metaObj := meta.New(walletStorage, TestChainID)

	vc := (&config.Voting{
		ProposalExpiration: 600 * time.Millisecond,
		MaxPendingRequests: 100,
	}).SetDefault()

	policyChan := make(chan policy.SigningPolicy, 1)
	instService := instruction.NewService(ctx, vc, teeID, policyChan, aq, metaObj, TestChainID, nil)
	external := server.NewExternal(fmt.Sprintf("%d", externalPort), instService, resultService, infoService, walletStorage, proxyPk, TestChainID, aq, server.DirectConfig{}, nil)

	wg.Go(func() {
		instService.Run(ctx)
	})

	wg.Go(func() {
		logger.Info("Starting external server")
		err := external.Serve()
		require.Error(t, err)
	})

	cleanup := func() {
		_ = internal.Close(ctx)
		_ = external.Close(ctx)
		cancel()
		logger.Info("Flushing redis")
		c.FlushAll(ctx)
		_ = c.Shutdown(ctx)
		mr.Close()
	}

	return &ProxyConfig{
		ExtPort:     externalPort,
		IntPort:     internalPort,
		TeeID:       teeID,
		TeePubKey:   teePub,
		ProxyPubKey: teePub,
		Aq:          aq,
		Rs:          rs,
		Vc:          vc,
		Pc:          policyChan,
		Ws:          walletStorage,
	}, cleanup
}

func makeRequests[T any](t *testing.T, url string, result *T) {
	t.Helper()
	start := time.Now()
	for {
		resp, err := http.Get(url)
		if err == nil && resp != nil {
			if resp.StatusCode == http.StatusOK {
				bodyBytes, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				err = resp.Body.Close()
				require.NoError(t, err)

				err = json.Unmarshal(bodyBytes, result)
				require.NoError(t, err)

				return
			}
		}
		if time.Since(start) > TestTimeConfig.Timeout {
			require.FailNow(t, fmt.Sprintf("Timeout waiting for %s", url))
			return
		}
		time.Sleep(TestTimeConfig.Interval)
	}
}

// GetTeeInfo Fetches TeeInfoResponse until TestTimeConfig.Timeout every TestTimeConfig.Interval
func GetTeeInfo(t *testing.T, pc *ProxyConfig) *types.TeeInfoResponse {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/info", pc.ExtPort)
	var res types.TeeInfoResponse
	makeRequests(t, url, &res)
	return &res
}

// GetVotingStatuses Fetches VoteStatus until TestTimeConfig.Timeout every TestTimeConfig.Interval
func GetVotingStatuses(t *testing.T, pc *ProxyConfig, rewardEpochID uint32, instructionID common.Hash) *voting.Statuses {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/action/status/%d/%s", pc.ExtPort, rewardEpochID, instructionID.String()[2:])
	var res voting.Statuses
	makeRequests(t, url, &res)
	return &res
}

// GetWalletInfo Fetches KeyData until TestTimeConfig.Timeout every TestTimeConfig.Interval
func GetWalletInfo(t *testing.T, pc *ProxyConfig, walletID [32]byte, keyID uint64) *pkgwallets.KeyData {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/wallet/%x/%d", pc.ExtPort, common.BytesToHash(walletID[:]), keyID)
	var res pkgwallets.KeyData
	makeRequests(t, url, &res)
	return &res
}
