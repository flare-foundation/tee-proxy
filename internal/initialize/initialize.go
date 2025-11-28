package initialize

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
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

func Initialize(ctx context.Context, cfgPath string) {
	cfg, err := config.Read(cfgPath)
	if err != nil {
		logger.Panicf("reading config: %v", err)
	}

	logger.Set(cfg.Logging)
	database.SetErrorLogger(logger.GetLogger())

	db, err := database.Connect(&cfg.DB)
	if err != nil {
		logger.Panicf("connecting to database: %v", err)
	}

	err = database.WaitCIndexerToSync(ctx, db, database.SyncParams{
		Retries:            30,
		OutOfSyncTolerance: 1 * time.Minute,
		MaxSleepTime:       10 * time.Minute,
		MinSleepTime:       1 * time.Second,
	}, logger.GetLogger())
	if err != nil {
		logger.Panicf("c-chain indexer: %v", err)
	}

	privKey, err := config.PrivateKeyFromEnv(cfg.PrivateKeyVariable)
	if err != nil {
		logger.Panicf("loading private key from env variable %s (default is %s): %v", cfg.PrivateKeyVariable, config.DefaultPrivateKeyVariable, err)
	}

	redisClient := storage.NewClient(cfg.RedisPort)

	actionQueues := queue.NewActionQueues(redisClient)
	resultStorage := result.NewStorage(redisClient)

	walletService := wallets.NewService(actionQueues, resultStorage, redisClient)
	resultService := result.NewService(resultStorage)

	infoService := new(info.Service)

	livenessService := liveness.New(db, redisClient, infoService)
	defer livenessService.SignalStartupFinished()

	internalServer := server.NewInternal(cfg.Ports.Internal, actionQueues, &resultService, &walletService, &livenessService)
	go internalServer.Serve() //nolint:errcheck // todo

	*infoService = info.NewService(db, actionQueues, resultStorage, &cfg.InfoTiming)

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
	go walletService.RunUpdateInfo(ctx, walletsSyncTrigger, resultService.BackupTrigger, resultService.WalletSync)
	go wallets.PeriodicWalletsSyncTrigger(ctx, walletsSyncTrigger, 60*time.Minute)

	policyService := policy.NewService(actionQueues, cfg.Addresses)
	err = policyService.Initialize(ctx, db, cfg.InitialSigningPolicyOffset, initialInfo)
	if err != nil {
		logger.Panicf("initializing signing policy: %v", err)
	}

	policyChan, err := policyService.Run(ctx, db, cfg.SigningPolicyFetchInterval)
	if err != nil {
		logger.Panicf("starting signing policy updater: %v", err)
	}

	meta := meta.New(&walletService)
	instructionService := instruction.NewService(&cfg.Voting, teeID, privKey, policyChan, actionQueues, meta)
	go instructionService.Run(ctx)

	externalServer := server.NewExternal(cfg.Ports.External, &instructionService, &resultService, infoService, &walletService, privKey)
	go externalServer.Serve() //nolint:errcheck // todo
}

// parseTeeID extracts the TEE ID from the TEE info as the address corresponding to the public key.
func parseTeeID(info *types.TeeInfoResponse) (common.Address, error) {
	teePub, err := types.ParsePubKey(info.TeeInfo.PublicKey)
	if err != nil {
		return common.Address{}, err
	}

	return crypto.PubkeyToAddress(*teePub), nil
}
