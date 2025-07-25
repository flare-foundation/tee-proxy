package info

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flare-foundation/tee-proxy/pkg/config"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/alicebob/miniredis/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/tee"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/stretchr/testify/require"
)

func InMemoryDB(t *testing.T, name string) (*gorm.DB, string) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

func CreateBlock(prevHash string, number uint64) (*database.Block, common.Hash) {
	hashInput := fmt.Sprintf("%s-%d", prevHash, number)
	hash := common.HexToHash(hashInput)
	timestamp := uint64(time.Now().Unix())
	return &database.Block{
		Hash:      fmt.Sprintf("%x", hash),
		Number:    number,
		Timestamp: timestamp,
	}, hash
}

func TestInsertBlock(t *testing.T) {
	db, _ := InMemoryDB(t, "choose")
	err := db.AutoMigrate(&database.Block{})
	require.NoError(t, err)

	var latestBlockHash common.Hash
	for i := uint64(1); i <= 3; i++ {
		block, hash := CreateBlock(fmt.Sprintf("%d", i), i)
		latestBlockHash = hash
		if err := db.Create(block).Error; err != nil {
			panic(err)
		}
	}

	mr := miniredis.RunT(t)
	c := queue.NewClient(mr.Addr())
	aq := queue.NewActionQueues(c)
	rs := queue.NewResultStorage(c)

	s := Storage{
		db:              db,
		actionQueues:    aq,
		responseStorage: rs,
		timingConfig: config.StorageTiming{
			CycleInternal:          10 * time.Millisecond,
			CycleQueueResponseWait: 10 * time.Millisecond,
		},
	}

	go func() {
		err := s.Run(t.Context())
		if err != nil {
			panic(err)
		}
	}()

	time.Sleep(15 * time.Millisecond)
	a, err := aq.Pop(t.Context(), queue.Read)
	require.NoError(t, err)
	require.Equal(t, types.Submit, a.Data.SubmissionTag)
	require.Equal(t, types.Direct, a.Data.Type)

	var instruction types.DirectInstructionData
	err = json.Unmarshal(a.Data.Message, &instruction)
	require.NoError(t, err)
	require.Equal(t, constants.Get.Hash(), instruction.OPType)
	require.Equal(t, constants.TEEInfo.Hash(), instruction.OPCommand)

	var data types.TeeInfoRequest
	err = json.Unmarshal(instruction.Message, &data)
	require.NoError(t, err)
	require.Equal(t, data.Challenge, latestBlockHash)

	resp := &types.TeeInfoResponse{
		TeeInfo: tee.TeeStructsAttestation{
			Challenge: latestBlockHash,
		},
		State:       []byte{},
		Attestation: hexutil.Bytes{},
	}
	m, err := json.Marshal(resp)
	require.NoError(t, err)

	ar := &types.ActionResponse{
		Result: types.ActionResult{
			ID:            a.Data.ID,
			SubmissionTag: a.Data.SubmissionTag,
			Status:        true,
			OPType:        constants.Get.Hash(),
			OPCommand:     constants.TEEInfo.Hash(),
			Version:       "1.0.0",
			Data:          m,
		},
	}
	require.NoError(t, err)
	err = rs.StoreResult(t.Context(), ar)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	require.NotNil(t, s.Latest)
	require.Equal(t, common.Hash(s.Latest.TeeInfo.Challenge), latestBlockHash)
}
