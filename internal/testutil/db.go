package testutil

import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
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
