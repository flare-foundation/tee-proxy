package initialize

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/instruction/meta"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

const (
	outOfSyncTolerance = 1 * time.Minute
	walletSyncPeriod   = 1 * time.Hour
	shutdownTimeout    = 10 * time.Second
)

func Initialize(ctx context.Context, cfgPath string) {
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
	walletService := wallets.NewService(actionQueues, resultStorage, backupIndex, backupStore, cfg.Storage.BackupTTL)
	resultService := result.NewService(resultStorage)

	infoService := info.NewService(db, actionQueues, resultStorage, &cfg.InfoTiming)

	livenessService := liveness.New(db, redisClient, infoService)

	internalServer := server.NewInternal(cfg.Ports.Internal, actionQueues, resultService, walletService, livenessService)
	go runServer("internal", internalServer.Serve)

	logger.Info("fetching initial TEE info")
	initialInfo, err := infoService.FetchInfo(ctx, cfg.InfoTiming.Initial)
	if err != nil {
		logger.Panicf("fetching initial TEE info: %v", err)
	}
	logger.Info("initial TEE info fetched")

	go infoService.Run(ctx) //nolint:errcheck // todo

	teeID, err := parseTeeID(initialInfo)
	if err != nil {
		logger.Panicf("parsing TEE id: %v", err)
	}
	err = resultService.SetIdentity(teeID)
	if err != nil {
		logger.Panicf("setting TEE identity: %v", err)
	}

	walletsSyncTrigger := make(chan bool, 1)
	go walletService.RunUpdateInfo(ctx, walletsSyncTrigger, resultService.BackupTrigger, resultService.KeyActions, resultService.Backups, resultService.KeyInfo)
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
	instructionService := instruction.NewService(ctx, &cfg.Voting, teeID, privKey, policyChan, actionQueues, meta)
	go instructionService.Run(ctx)

	directCfg := server.DirectConfig{
		Enable:   cfg.Direct.Enable,
		APIKey:   cfg.Direct.APIKey,
		NoAPIKey: cfg.Direct.NoAPIKey,
	}
	externalServer := server.NewExternal(cfg.Ports.External, &instructionService, resultService, infoService, walletService, privKey, actionQueues, directCfg)
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
