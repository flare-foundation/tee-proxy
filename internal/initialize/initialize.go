package initialize

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/database"
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
	"github.com/flare-foundation/tee-proxy/pkg/voting"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
)

func Initialize(ctx context.Context, cfgPath string) {
	cfg, err := config.Read(cfgPath)
	if err != nil {
		panic(err)
	}

	db, err := database.Connect(&cfg.DB)
	if err != nil {
		panic(err)
	}

	pk, err := config.PrivateKeyFromEnv(cfg.PrivateKeyVariable)
	if err != nil {
		panic(err)
	}

	redisClient := queue.NewClient(cfg.RedisPort)

	actionQueues := queue.NewActionQueues(redisClient)
	responseStorage := queue.NewResultStorage(redisClient)

	walletStorage := wallets.NewStorage(actionQueues, responseStorage)
	actionService := action.NewService(actionQueues)
	resultService := result.NewService(responseStorage)

	internalServer := server.NewInternal(cfg.Ports.Internal, &actionService, &resultService, &walletStorage)

	go internalServer.Serve() //nolint:errcheck // todo

	go walletStorage.RunInfo(ctx, resultService.WalletSyncTrigger)

	infoStorage := info.NewStorage(db, actionQueues, responseStorage, &cfg.StorageTiming)

	initialInfo, err := infoStorage.FetchInfo(ctx)
	if err != nil {
		panic(err)
	}

	go infoStorage.Run(ctx) //nolint:errcheck // todo

	teePub, err := types.ParsePubKey(initialInfo.TeeInfo.PublicKey)
	if err != nil {
		panic(err)
	}

	teeID := crypto.PubkeyToAddress(*teePub)

	err = resultService.SetIdentity(teeID)
	if err != nil {
		panic(err)
	}

	policyService := policy.NewService(actionQueues, cfg.Addresses)

	if (common.Hash{}).Cmp(initialInfo.TeeInfo.InitialSigningPolicyHash) != 0 {
		err = policyService.SetInitialPolicy(ctx, db, initialInfo.TeeInfo.LastSigningPolicyId)
		if err != nil {
			panic(err)
		}
		resultService.WalletSyncTrigger <- true
	} else {
		err = policyService.Initialize(ctx, db, cfg.Timing)
		if err != nil {
			panic(err)
		}
	}

	policyChan, err := policyService.Run(ctx, db)
	if err != nil {
		panic(err)
	}

	meta := meta.New(&walletStorage)

	vc := &voting.Config{
		ProposalExpiration: 2 * time.Second,
		MaxPendingRequests: 100,
	}

	vs := voting.NewStorage(vc, 3, meta, 10) //todo size
	instructionService := instruction.NewService(teeID, pk, policyChan, actionQueues, vs)

	externalServer := server.NewExternal(cfg.Ports.External, &instructionService, &actionService, &resultService, &infoStorage, &walletStorage)

	go externalServer.Serve() //nolint:errcheck // todo
}
