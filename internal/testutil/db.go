package testutil

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var dbCounter atomic.Uint64

func InMemoryDB(t *testing.T, name string) (*gorm.DB, string) {
	t.Helper()

	id := dbCounter.Add(1)
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", name, id)
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
