package initialize

import (
	"context"

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
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
)

func Initialize(cfgPath string) {
	cfg, err := config.Read(cfgPath)
	if err != nil {
		panic(err)
	}

	db, err := database.Connect(&cfg.DB)
	if err != nil {
		panic(err)
	}

	ctx := context.TODO()

	redisClient := queue.NewClient(cfg.RedisPort)

	actionQueues := queue.NewActionQueues(redisClient)
	responseStorage := queue.NewResponseStorage(redisClient)

	walletStorage := wallets.NewStorage(actionQueues, responseStorage)
	actionService := action.NewService(actionQueues)
	resultService := result.NewService(responseStorage)

	internalServer := server.NewInternal(cfg.Ports.Internal, &actionService, &resultService, &walletStorage)

	go internalServer.Serve() //nolint:errcheck // todo

	go walletStorage.RunInfo(ctx, resultService.WalletSyncTrigger)

	infoStorage := info.NewStorage(db, actionQueues, responseStorage)

	initialInfo, err := infoStorage.InitialInfo(ctx)
	if err != nil {
		panic(err)
	}

	go infoStorage.Run(ctx) //nolint:errcheck // todo

	teePub, err := types.ParsePubKey(initialInfo.PublicKey)
	if err != nil {
		panic(err)
	}

	teeID := crypto.PubkeyToAddress(*teePub)

	pk, err := crypto.ToECDSA(cfg.PrivKey)
	if err != nil {
		panic(err)
	}

	policyService := policy.NewService(actionQueues, cfg.Addresses)

	if initialInfo.InitialSigningPolicyHash.Cmp(common.Hash{}) == 0 {
		policyService.SetInitialPolicy(ctx, db, initialInfo.LastSigningPolicyId)
	} else {
		err = policyService.Initialize(ctx, db)
		if err != nil {
			panic(err)
		}
	}

	policyChan, err := policyService.Run(ctx, db)
	if err != nil {
		panic(err)
	}

	meta := meta.New(&walletStorage)

	instructionService := instruction.NewService(teeID, pk, policyChan, actionQueues, meta)

	externalServer := server.NewExternal(cfg.Ports.External, &instructionService, &actionService, &resultService, &infoStorage, &walletStorage)

	go externalServer.Serve() //nolint:errcheck // todo
}
