package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
	actionQueues := queue.NewActionQueues(redisClient, cfg.Storage.ActionTTL)

	var (
		resultStore storage.Storage[*types.ActionResponse]
		backupStore storage.Storage[*teewallets.TEEBackupResponse]
		backupIndex storage.Storage[common.Hash]
	)

	if (cfg.Firestore != config.Firestore{}) {
		fbClient, err := storage.NewFirestoreClient(ctx, cfg.Firestore.ProjectID, cfg.Firestore.DatabaseID, cfg.Firestore.CredentialsFile, cfg.Firestore.URL)
		if err != nil {
			logger.Panicf("connecting to Firestore: %v", err)
		}
		resultStore = storage.NewFirestoreStorage[*types.ActionResponse](fbClient, "results")
		backupStore = storage.NewFirestoreStorage[*teewallets.TEEBackupResponse](fbClient, "backups")
		backupIndex = storage.NewFirestoreStorage[common.Hash](fbClient, "backupIndex")
	} else {
		resultStore = storage.NewRedisStorage[*types.ActionResponse]("results", redisClient)
		backupStore = storage.NewRedisStorage[*teewallets.TEEBackupResponse]("backups", redisClient)
		backupIndex = storage.NewRedisStorage[common.Hash]("backupIndex", redisClient)
	}

	resultStorage := result.NewStorage(resultStore, storage.NewNotifier(redisClient), cfg.Storage.ResultTTL, cfg.Storage.SubmitResultTTL)
	resultService := result.NewService(resultStorage)
	walletService := wallets.NewService(actionQueues, resultStorage, backupIndex, backupStore, cfg.Storage.BackupTTL)

	attestationCfg, err := buildAttestationConfig(&cfg.Attestation)
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

	teeID, err := parseTeeID(initialInfo)
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

	policyService := policy.NewService(actionQueues, cfg.Addresses, cfg.ChainID)
	err = policyService.Initialize(ctx, db, cfg.InitialSigningPolicyOffset, initialInfo)
	if err != nil {
		logger.Panicf("initializing signing policy: %v", err)
	}

	policyChan, err := policyService.Run(ctx, db, cfg.SigningPolicyFetchInterval)
	if err != nil {
		logger.Panicf("starting signing policy updater: %v", err)
	}

	meta := meta.New(walletService)
	instructionService := instruction.NewService(ctx, &cfg.Voting, teeID, policyChan, actionQueues, meta)
	go instructionService.Run(ctx)

	directCfg := server.DirectConfig{
		Enable:         cfg.Direct.Enable,
		APIKey:         cfg.Direct.APIKey,
		APIKeyOptional: cfg.Direct.APIKeyOptional,
	}
	if cfg.Direct.Enable && cfg.Direct.APIKeyOptional {
		logger.Warn("/direct is enabled without API key authentication (api_key_optional = true)")
	}
	externalServer := server.NewExternal(cfg.Ports.External, instructionService, resultService, infoService, walletService, privKey, actionQueues, directCfg)
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

// parseTeeID extracts the TEE ID from the TEE info as the address corresponding to the public key.
func parseTeeID(info *types.TeeInfoResponse) (common.Address, error) {
	teePub, err := types.ParsePubKey(info.TeeInfo.PublicKey)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*teePub), nil
}

func buildAttestationConfig(cfg *config.Attestation) (*attestation.Config, error) {
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
		ExpectedCodeHash:    codeHashes,
		ExpectedPlatform:    platforms,
		ExpectedDebugStatus: cfg.ExpectedDebugStatuses,
		MaxTokenAge:         cfg.MaxTokenAge,
		RequireSecBoot:      cfg.RequireSecBoot,
		AllowMagicPass:      cfg.AllowMagicPass,
	}, nil
}

func logAttestationPosture(cfg *attestation.Config) {
	if !cfg.Enabled {
		logger.Warn("attestation verification disabled — bootstrap relies on network isolation only")
		return
	}
	a := cfg.Active()
	logger.Infof("attestation verification enabled: code_hash=%v platform=%v debug_status=%v max_token_age=%v sec_boot=%v magic_pass=%v",
		a.CodeHash, a.Platform, a.DebugStatus, a.MaxTokenAge, a.SecBoot, a.MagicPass)
	if cfg.AllowMagicPass {
		logger.Warn("attestation: allow_magic_pass=true — accepts the tee-node magic_pass sentinel in place of a real JWT; do not enable in production")
	}
}
