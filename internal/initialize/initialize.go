package initialize

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/service/action"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/policy"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/service/server"
	"github.com/flare-foundation/tee-proxy/pkg/config"
	"github.com/flare-foundation/tee-proxy/pkg/info"
	"github.com/flare-foundation/tee-proxy/pkg/meta"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
)

func Initialize(ctx context.Context, cfgPath string) {
	cfg, err := config.Read(cfgPath)
	if err != nil {
		logger.Panicf("reading config: %v", err)
	}

	db, err := database.Connect(&cfg.DB)
	if err != nil {
		logger.Panicf("connecting to database: %v", err)
	}

	database.WaitCIndexerToSync(ctx, db, database.SyncParams{
		Retries:            30,
		OutOfSyncTolerance: 24 * 365 * time.Hour, // temporary, so we can do test with an old database
		MaxSleepTime:       10 * time.Minute,
		MinSleepTime:       1 * time.Second,
	})

	privKey, err := config.PrivateKeyFromEnv(cfg.PrivateKeyVariable)
	if err != nil {
		logger.Panicf("loading private key from env variable %s (default is %s): %v", cfg.PrivateKeyVariable, config.DefaultPrivateKeyVariable, err)
	}

	redisClient := storage.NewClient(cfg.RedisPort)

	actionQueues := queue.NewActionQueues(redisClient)
	responseStorage := queue.NewResultStorage(redisClient)

	walletStorage := wallets.NewStorage(actionQueues, responseStorage, redisClient)
	actionService := action.NewService(actionQueues)
	resultService := result.NewService(responseStorage)

	internalServer := server.NewInternal(cfg.Ports.Internal, &actionService, &resultService, &walletStorage)

	go internalServer.Serve() //nolint:errcheck // todo

	walletSyncCue := make(chan bool, 1)

	go walletStorage.RunInfo(ctx, walletSyncCue, resultService.BackupTrigger, resultService.WalletSync)

	infoStorage := info.NewStorage(db, actionQueues, responseStorage, &cfg.StorageTiming)

	initialInfo, err := infoStorage.FetchInfo(ctx)
	if err != nil {
		logger.Panicf("fetching initial TEE info: %v", err)
	}

	go infoStorage.Run(ctx) //nolint:errcheck // todo

	teePub, err := types.ParsePubKey(initialInfo.TeeInfo.PublicKey)
	if err != nil {
		logger.Panicf("parsing TEE public key: %v", err)
	}

	teeID := crypto.PubkeyToAddress(*teePub)

	err = resultService.SetIdentity(teeID)
	if err != nil {
		logger.Panicf("setting TEE identity: %v", err)
	}

	policyService := policy.NewService(actionQueues, cfg.Addresses)

	if initialInfo.TeeInfo.InitialSigningPolicyHash.Cmp(common.Hash{}) != 0 {
		logger.Infof("starting signing policy updates from epoch %d", initialInfo.TeeInfo.LastSigningPolicyID)
		err = policyService.SetInitialPolicy(ctx, db, initialInfo.TeeInfo.LastSigningPolicyID)
		if err != nil {
			logger.Panicf("setting initial signing policy: %v", err)
		}
		walletSyncCue <- true
	} else {
		logger.Info("initializing signing policy")
		err = policyService.Initialize(ctx, db, cfg.InitialSigningPolicyOffset)
		if err != nil {
			logger.Panicf("initializing signing policy: %v", err)
		}
	}

	policyChan, err := policyService.Run(ctx, db, cfg.SigningPolicyFetchInterval)
	if err != nil {
		logger.Panicf("starting signing policy updater: %v", err)
	}

	meta := meta.New(&walletStorage)

	vs := instruction.NewStorage(&cfg.Voting, 3, meta, 10) //todo size
	instructionService := instruction.NewService(teeID, privKey, policyChan, actionQueues, vs)
	go instructionService.Forward(ctx)          //nolint:errcheck // todo
	go instructionService.ListenToPolicies(ctx) //nolint:errcheck // todo

	externalServer := server.NewExternal(cfg.Ports.External, &instructionService, &actionService, &resultService, &infoStorage, &walletStorage, privKey)

	go externalServer.Serve() //nolint:errcheck // todo

	go walletSyncTrigger(ctx, walletSyncCue)
}

func walletSyncTrigger(ctx context.Context, c chan bool) {
	for {
		if ctx.Err() != nil {
			return
		}

		c <- true

		time.Sleep(60 * time.Minute)
	}
}
