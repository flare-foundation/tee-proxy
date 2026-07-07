package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
	teewallets "github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/flare-foundation/tee-proxy/internal/liveness"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/server"
	"github.com/flare-foundation/tee-proxy/internal/service/info"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/machinepath"
	"github.com/flare-foundation/tee-proxy/internal/service/policy"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/attestation"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

const (
	outOfSyncTolerance = 1 * time.Minute
	walletSyncPeriod   = 1 * time.Hour
	shutdownTimeout    = 10 * time.Second
)

// Run boots the proxy and blocks until ctx is cancelled, then drains the HTTP servers.
func Run(ctx context.Context, cfgPath string) {
	cfg, err := config.Read(cfgPath)
	if err != nil {
		logger.Panicf("reading config: %v", err)
	}

	logger.Set(cfg.Logging)
	database.SetErrorLogger(logger.Logger())

	db, err := database.Connect(&cfg.DB)
	if err != nil {
		logger.Panicf("connecting to database: %v", err)
	}

	err = database.WaitCIndexerToSync(ctx, db, database.SyncParams{
		Retries:            30,
		MaxSleepTime:       cfg.DBSyncMaxSleepTime,
		OutOfSyncTolerance: outOfSyncTolerance,
		MinSleepTime:       1 * time.Second,
	}, logger.Logger())
	if err != nil {
		logger.Panicf("c-chain indexer: %v", err)
	}

	privKey, err := config.PrivateKeyFromEnv(cfg.PrivateKeyVariable)
	if err != nil {
		logger.Panicf("loading private key from env variable %s (default is %s): %v", cfg.PrivateKeyVariable, config.DefaultPrivateKeyVariable, err)
	}

	redisClient := storage.NewClient(cfg.RedisPort)
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Warnf("closing Redis client: %v", err)
		}
	}()
	proxyMetrics := metrics.New(metricsConfig(cfg.Metrics))

	actionQueues := queue.NewActionQueues(redisClient, cfg.Storage.ActionTTL, proxyMetrics)

	var (
		resultStore    storage.Storage[*types.ActionResponse]
		backupStore    storage.Storage[*teewallets.TEEBackupResponse]
		backupIndex    storage.Storage[common.Hash]
		storageBackend string
	)

	if (cfg.Firestore != config.Firestore{}) {
		fbClient, err := storage.NewFirestoreClient(ctx, cfg.Firestore.ProjectID, cfg.Firestore.DatabaseID, cfg.Firestore.CredentialsFile, cfg.Firestore.URL)
		if err != nil {
			logger.Panicf("connecting to Firestore: %v", err)
		}
		defer func() {
			if err := fbClient.Close(); err != nil {
				logger.Warnf("closing Firestore client: %v", err)
			}
		}()
		resultStore = storage.NewFirestoreStorage[*types.ActionResponse](fbClient, "results")
		backupStore = storage.NewFirestoreStorage[*teewallets.TEEBackupResponse](fbClient, "backups")
		backupIndex = storage.NewFirestoreStorage[common.Hash](fbClient, "backupIndex")
		storageBackend = "firestore"
	} else {
		resultStore = storage.NewRedisStorage[*types.ActionResponse]("results", redisClient)
		backupStore = storage.NewRedisStorage[*teewallets.TEEBackupResponse]("backups", redisClient)
		backupIndex = storage.NewRedisStorage[common.Hash]("backupIndex", redisClient)
		storageBackend = "redis"
	}

	if obs := proxyMetrics.StorageObserver(); obs != nil {
		resultStore = storage.WithMetrics(resultStore, obs, storageBackend, "results")
		backupStore = storage.WithMetrics(backupStore, obs, storageBackend, "backups")
		backupIndex = storage.WithMetrics(backupIndex, obs, storageBackend, "backupIndex")
	}

	resultStorage := result.NewStorage(resultStore, storage.NewNotifier(redisClient), cfg.Storage.ResultTTL, cfg.Storage.SubmitResultTTL)
	resultService := result.NewService(resultStorage, cfg.ChainID, proxyMetrics)
	walletService := wallets.NewService(actionQueues, resultStorage, backupIndex, backupStore, cfg.Storage.BackupTTL, proxyMetrics)

	attestationCfg, err := buildAttestationConfig(&cfg.Attestation, cfg.ChainID)
	if err != nil {
		logger.Panicf("building attestation config: %v", err)
	}
	logAttestationPosture(attestationCfg)

	infoService := info.NewService(db, actionQueues, resultStorage, &cfg.InfoTiming, attestationCfg, proxyMetrics)

	livenessService := liveness.New(db, redisClient, infoService, resultService, proxyMetrics)

	internalServer := server.NewInternal(cfg.Ports.Internal, actionQueues, resultService, walletService, livenessService, proxyMetrics)
	go runServer("internal", internalServer.Serve)

	logger.Info("fetching initial TEE info")
	initialInfo, sentChallenge, err := infoService.FetchInfo(ctx, cfg.InfoTiming.Initial)
	if err != nil {
		logger.Panicf("fetching initial TEE info: %v", err)
	}
	logger.Info("initial TEE info fetched")

	// Challenge round-trip check runs unconditionally; attestation.Verify (called inside
	// FetchInfo) duplicates this when enabled but skips it when disabled.
	if initialInfo.TeeInfo.Challenge != sentChallenge {
		logger.Panicf("TEE info challenge mismatch: sent %s, received %s", sentChallenge, initialInfo.TeeInfo.Challenge)
	}

	go func() {
		if err := infoService.Run(ctx); err != nil {
			logger.Errorf("info service exited: %v", err)
		}
	}()

	teeID, err := info.ParseTeeID(initialInfo)
	if err != nil {
		logger.Panicf("parsing TEE id: %v", err)
	}
	err = resultService.SetIdentity(teeID)
	if err != nil {
		logger.Panicf("setting TEE identity: %v", err)
	}

	walletsSyncTrigger := make(chan bool, 1)
	go walletService.RunUpdateInfo(ctx, walletsSyncTrigger, resultService.BackupTrigger, resultService.KeyActions, resultService.Backups)
	go wallets.PeriodicWalletsSyncTrigger(ctx, walletsSyncTrigger, walletSyncPeriod)

	policyService := policy.NewService(actionQueues, resultStorage, cfg.Addresses, cfg.ChainID, infoService, proxyMetrics)
	err = policyService.Initialize(ctx, db, cfg.InitialSigningPolicyOffset, initialInfo)
	if err != nil {
		logger.Panicf("initializing signing policy: %v", err)
	}

	policyChan, err := policyService.Run(ctx, db, cfg.SigningPolicyFetchInterval)
	if err != nil {
		logger.Panicf("starting signing policy updater: %v", err)
	}

	if cfg.Addresses.MachinePathManager != (common.Address{}) {
		machinePathService := machinepath.NewService(actionQueues, resultStorage, cfg.Addresses.MachinePathManager, initialInfo.MachineData.ExtensionID, cfg.ChainID, proxyMetrics)
		machinePathService.Run(ctx, db, cfg.MachinePathListFetchInterval)
	} else {
		logger.Info("machine_path_manager address not set; machine path list service disabled")
	}

	meta := meta.New(walletService, cfg.ChainID)
	instructionService := instruction.NewService(ctx, &cfg.Voting, teeID, policyChan, actionQueues, meta, cfg.ChainID, proxyMetrics)
	go instructionService.Run(ctx)

	directCfg := directConfig(cfg.Direct)
	if cfg.Direct.Enable && cfg.Direct.APIKeyOptional {
		logger.Warn("/direct is enabled without API key authentication (api_key_optional = true)")
	}
	externalServer := server.NewExternal(cfg.Ports.External, instructionService, resultService, infoService, walletService, privKey, cfg.ChainID, actionQueues, directCfg, proxyMetrics)
	go runServer("external", externalServer.Serve)

	livenessService.SignalStartupFinished()

	// Block until shutdown is signalled via ctx, then drain the HTTP servers.
	<-ctx.Done()
	logger.Info("context cancelled, shutting down HTTP servers")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := internalServer.Close(shutdownCtx); err != nil {
		logger.Warnf("shutting down internal server: %v", err)
	}
	if err := externalServer.Close(shutdownCtx); err != nil {
		logger.Warnf("shutting down external server: %v", err)
	}
}

// runServer invokes serve and panics if it returns an error other than http.ErrServerClosed,
// so the process exits and the container restarts instead of silently running without an HTTP server.
func runServer(name string, serve func() error) {
	err := serve()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Panicf("%s server stopped: %v", name, err)
	}
}

// directConfig maps the /direct endpoint configuration onto the server's DirectConfig,
// including the body-size limit applied to POST /direct requests.
func directConfig(c config.Direct) server.DirectConfig {
	return server.DirectConfig{
		Enable:         c.Enable,
		APIKey:         c.APIKey,
		APIKeyOptional: c.APIKeyOptional,
		MaxBodySize:    c.MaxBodySize,
	}
}

// metricsConfig resolves config.Metrics into metrics.Config: with Enable false every
// group is off; otherwise an unset group inherits Enable and an explicit value wins.
func metricsConfig(c config.Metrics) metrics.Config {
	group := func(p *bool) bool {
		if !c.Enable {
			return false
		}
		return p == nil || *p
	}

	return metrics.Config{
		Enable:       c.Enable,
		HTTP:         group(c.HTTP),
		Storage:      group(c.Storage),
		Queue:        group(c.Queue),
		Voting:       group(c.Voting),
		ActiveVoters: group(c.ActiveVoters),
		Result:       group(c.Result),
		Info:         group(c.Info),
		Attestation:  group(c.Attestation),
		Policy:       group(c.Policy),
		Liveness:     group(c.Liveness),
		Node:         group(c.Node),
		Runtime:      group(c.Runtime),
	}
}

func buildAttestationConfig(cfg *config.Attestation, chainID uint64) (*attestation.Config, error) {
	if !cfg.Enable {
		return &attestation.Config{Enabled: false}, nil
	}

	root, err := attestation.GoogleCSRoot()
	if err != nil {
		return nil, fmt.Errorf("loading Google CS root: %w", err)
	}

	codeHashes, err := cfg.ParsedCodeHashes()
	if err != nil {
		return nil, fmt.Errorf("parsing expected_code_hashes: %w", err)
	}

	platforms := make([]common.Hash, 0, len(cfg.ExpectedPlatforms))
	for _, p := range cfg.ExpectedPlatforms {
		h, err := convert.StringToCommonHash(p)
		if err != nil {
			return nil, fmt.Errorf("encoding expected_platforms %q: %w", p, err)
		}
		platforms = append(platforms, h)
	}

	return &attestation.Config{
		Enabled:             true,
		RootCert:            root,
		Audience:            cfg.Audience,
		ExpectedCodeHash:    codeHashes,
		ExpectedPlatform:    platforms,
		ExpectedDebugStatus: cfg.ExpectedDebugStatuses,
		MaxTokenAge:         cfg.MaxTokenAge,
		RequireSecBoot:      cfg.RequireSecBoot,
		AllowMagicPass:      cfg.AllowMagicPass,
		ChainID:             chainID,
	}, nil
}

func logAttestationPosture(cfg *attestation.Config) {
	if !cfg.Enabled {
		logger.Warn("attestation verification disabled — bootstrap relies on network isolation only")
		return
	}
	a := cfg.Active()
	logger.Infof("attestation verification enabled: audience=%v code_hash=%v platform=%v debug_status=%v max_token_age=%v sec_boot=%v magic_pass=%v",
		a.Audience, a.CodeHash, a.Platform, a.DebugStatus, a.MaxTokenAge, a.SecBoot, a.MagicPass)
	if cfg.AllowMagicPass {
		logger.Warn("attestation: allow_magic_pass=true — accepts the tee-node magic_pass sentinel in place of a real JWT; do not enable in production")
	}
}
