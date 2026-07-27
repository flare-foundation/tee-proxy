package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/convert"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
	teewallets "github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/flare-foundation/tee-proxy/internal/liveness"
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
	actionQueues := queue.NewActionQueues(redisClient, cfg.Storage.ActionTTL)

	var (
		resultStore storage.Storage[*types.ActionResponse]
		backupStore storage.Storage[*teewallets.TEEBackupResponse]
		backupIndex storage.Storage[common.Hash]
	)

	if cfg.GCS.Bucket != "" {
		if cfg.GCS.URL != "" {
			logger.Warnf("GCS endpoint override %q is active: the connection is UNAUTHENTICATED (emulator mode, not for production)", cfg.GCS.URL)
		}
		gcsClient, err := storage.NewGCSClient(ctx, cfg.GCS.CredentialsFile, cfg.GCS.URL)
		if err != nil {
			logger.Panicf("connecting to Google Cloud Storage: %v", err)
		}
		defer func() {
			if err := gcsClient.Close(); err != nil {
				logger.Warnf("closing Google Cloud Storage client: %v", err)
			}
		}()
		resultStore = storage.NewGCSStorage[*types.ActionResponse](gcsClient, cfg.GCS.Bucket, path.Join(cfg.GCS.Prefix, "results"))
		backupStore = storage.NewGCSStorage[*teewallets.TEEBackupResponse](gcsClient, cfg.GCS.Bucket, path.Join(cfg.GCS.Prefix, "backups"))
		backupIndex = storage.NewGCSStorage[common.Hash](gcsClient, cfg.GCS.Bucket, path.Join(cfg.GCS.Prefix, "backupIndex"))
		logger.Infof("persistent storage backend: GCS bucket %q prefix %q", cfg.GCS.Bucket, cfg.GCS.Prefix)
	} else {
		resultStore = storage.NewRedisStorage[*types.ActionResponse]("results", redisClient)
		backupStore = storage.NewRedisStorage[*teewallets.TEEBackupResponse]("backups", redisClient)
		backupIndex = storage.NewRedisStorage[common.Hash]("backupIndex", redisClient)
		logger.Info("persistent storage backend: Redis (gcs bucket not configured)")
	}

	resultStorage := result.NewStorage(resultStore, storage.NewNotifier(redisClient), cfg.Storage.ResultTTL, cfg.Storage.SubmitResultTTL)
	resultService := result.NewService(resultStorage, cfg.ChainID)
	walletService := wallets.NewService(actionQueues, resultStorage, backupIndex, backupStore, cfg.Storage.BackupTTL)

	attestationCfg, err := buildAttestationConfig(&cfg.Attestation, cfg.ChainID)
	if err != nil {
		logger.Panicf("building attestation config: %v", err)
	}
	logAttestationPosture(attestationCfg)

	infoService := info.NewService(db, actionQueues, resultStorage, &cfg.InfoTiming, attestationCfg)

	livenessService := liveness.New(db, redisClient, infoService, resultService)

	internalServer := server.NewInternal(cfg.Ports.Internal, actionQueues, resultService, walletService, livenessService)
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

	policyService := policy.NewService(actionQueues, resultStorage, cfg.Addresses, cfg.ChainID, infoService)
	err = policyService.Initialize(ctx, db, cfg.InitialSigningPolicyOffset, initialInfo)
	if err != nil {
		logger.Panicf("initializing signing policy: %v", err)
	}

	policyChan, err := policyService.Run(ctx, db, cfg.SigningPolicyFetchInterval)
	if err != nil {
		logger.Panicf("starting signing policy updater: %v", err)
	}

	if cfg.Addresses.MachinePathManager != (common.Address{}) {
		governance, err := resolveGovernance(cfg.Governance, initialInfo.MachineData.GovernanceHash)
		if err != nil {
			logger.Panicf("governance configuration: %v", err)
		}
		machinePathService := machinepath.NewService(actionQueues, resultStorage, cfg.Addresses.MachinePathManager, governance, initialInfo.MachineData.ExtensionID, cfg.ChainID, initialInfo.TeeInfo.MachinePathListNonce)
		machinePathService.Run(ctx, db, cfg.MachinePathListFetchInterval)
	} else {
		logger.Info("machine_path_manager address not set; machine path list service disabled")
	}

	meta := meta.New(walletService, cfg.ChainID)
	instructionService := instruction.NewService(ctx, &cfg.Voting, teeID, policyChan, actionQueues, meta, cfg.ChainID)
	go instructionService.Run(ctx)

	directCfg := directConfig(cfg.Direct)
	if cfg.Direct.Enable && cfg.Direct.APIKeyOptional {
		logger.Warn("/direct is enabled without API key authentication (api_key_optional = true)")
	}
	externalServer := server.NewExternal(cfg.Ports.External, instructionService, resultService, infoService, walletService, privKey, cfg.ChainID, actionQueues, directCfg)
	go runServer("external", externalServer.Serve)

	livenessService.SignalStartupFinished()

	// Block until shutdown is signalled via ctx, then drain the HTTP servers.
	<-ctx.Done()
	logger.Info("context cancelled, shutting down HTTP servers")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Parallel: the internal server's wait for result writes must not eat the
	// external server's drain budget.
	var closes sync.WaitGroup
	closes.Go(func() {
		if err := internalServer.Close(shutdownCtx); err != nil {
			logger.Warnf("shutting down internal server: %v", err)
		}
	})
	closes.Go(func() {
		if err := externalServer.Close(shutdownCtx); err != nil {
			logger.Warnf("shutting down external server: %v", err)
		}
	})
	closes.Wait()
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
