package policy

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/entitymanager"

	"github.com/flare-foundation/go-flare-common/pkg/contracts/preregistry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/registry"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/relay"
	"github.com/flare-foundation/go-flare-common/pkg/database"
	"gorm.io/gorm"
)

var (
	signingPolicyInitializedEventSel                  common.Hash
	voterRegisteredEventSel                           common.Hash
	voterPreRegisteredEventSel                        common.Hash
	signingPolicyAddressRegistrationConfirmedEventSel common.Hash
)

func init() {
	relayABI, err := relay.RelayMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get relayABI: %w", err))
	}

	signingPolicyEvent, ok := relayABI.Events["SigningPolicyInitialized"]
	if !ok {
		panic(fmt.Errorf("cannot get SigningPolicyInitialized event: %w", err))
	}
	signingPolicyInitializedEventSel = signingPolicyEvent.ID

	voterRegistryABI, err := registry.RegistryMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get voterRegistryABI: %w", err))
	}

	voterRegisteredEvent, ok := voterRegistryABI.Events["VoterRegistered"]
	if !ok {
		panic(fmt.Errorf("cannot get VoterRegistered event: %w", err))
	}

	voterRegisteredEventSel = voterRegisteredEvent.ID

	voterPreRegistryABI, err := preregistry.PreregistryMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get voterPreRegistryABI: %w", err))
	}

	voterPreRegisteredEvent, ok := voterPreRegistryABI.Events["VoterPreRegistered"]
	if !ok {
		panic(fmt.Errorf("cannot get VoterPreRegistered event: %w", err))
	}

	voterPreRegisteredEventSel = voterPreRegisteredEvent.ID

	entityManagerABI, err := entitymanager.EntityManagerMetaData.GetAbi()
	if err != nil {
		panic(fmt.Errorf("cannot get entityManagerABI: %w", err))
	}

	signingPolicyAddressRegistrationConfirmedEvent, ok := entityManagerABI.Events["SigningPolicyAddressRegistrationConfirmed"]
	if !ok {
		panic(fmt.Errorf("cannot get SigningPolicyAddressRegistrationConfirmed event: %w", err))
	}

	signingPolicyAddressRegistrationConfirmedEventSel = signingPolicyAddressRegistrationConfirmedEvent.ID

}

// fetchSigningPolicyInitializedEventLogs
func fetchSigningPolicyInitializedEventLogs(ctx context.Context, db *gorm.DB, relayContractAddress common.Address, initialSigningPolicyID uint32) ([]database.Log, error) {
	address := hex.EncodeToString(relayContractAddress[:])
	topic0 := hex.EncodeToString(signingPolicyInitializedEventSel[:])
	topic1Bytes := make([]byte, 32)
	binary.BigEndian.PutUint32(topic1Bytes[28:], initialSigningPolicyID)
	topic1 := hex.EncodeToString(topic1Bytes)

	var logs []database.Log

	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 >= ?", address, topic0, topic1).Order("timestamp").Find(&logs).Error // todo add retry

	return logs, err
}

func fetchVoterRegistered(ctx context.Context, db *gorm.DB, voterRegistryAddress common.Address, signingPolicyID uint32, signingPolicyAddress common.Address) ([]database.Log, error) {
	address := hex.EncodeToString(voterRegistryAddress[:])
	topic0 := hex.EncodeToString(signingPolicyInitializedEventSel[:])
	topic1Bytes := make([]byte, 32)
	binary.BigEndian.PutUint32(topic1Bytes[28:], signingPolicyID)
	topic1 := hex.EncodeToString(topic1Bytes)

	topic2Bytes := make([]byte, 32)
	copy(topic2Bytes[12:], signingPolicyAddress[:])
	topic2 := hex.EncodeToString(topic2Bytes)

	var logs []database.Log
	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 = ? AND topic2 = ?", address, topic0, topic1, topic2).Find(&logs).Error // todo add retry

	return logs, err
}

func fetchVoterPreRegistered(ctx context.Context, db *gorm.DB, voterPreRegistryAddress common.Address, signingPolicyID uint32, identityAddress common.Address) ([]database.Log, error) {
	address := hex.EncodeToString(voterPreRegistryAddress[:])

	topic0 := hex.EncodeToString(voterPreRegisteredEventSel[:])

	topic1Bytes := make([]byte, 32)
	copy(topic1Bytes[12:], identityAddress[:])
	topic1 := hex.EncodeToString(topic1Bytes)

	topic2Bytes := make([]byte, 32)
	binary.BigEndian.PutUint32(topic2Bytes[28:], signingPolicyID)
	topic2 := hex.EncodeToString(topic2Bytes)

	var logs []database.Log
	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 = ? AND topic2 = ?", address, topic0, topic1, topic2).Find(&logs).Error // todo add retry

	return logs, err
}

func fetchSigningPolicyAddressRegistrationConfirmed(ctx context.Context, db *gorm.DB, entityManagerAddress common.Address, identityAddress, signingPolicyAddress common.Address) ([]database.Log, error) {
	address := hex.EncodeToString(entityManagerAddress[:])

	topic0 := hex.EncodeToString(signingPolicyAddressRegistrationConfirmedEventSel[:])

	topic1Bytes := make([]byte, 32)
	copy(topic1Bytes[12:], identityAddress[:])
	topic1 := hex.EncodeToString(topic1Bytes)

	topic2Bytes := make([]byte, 32)
	copy(topic2Bytes[12:], signingPolicyAddress[:])
	topic2 := hex.EncodeToString(topic2Bytes)

	var logs []database.Log
	err := db.WithContext(ctx).Where("address = ? AND topic0 = ? AND topic1 = ? AND topic2 = ?", address, topic0, topic1, topic2).Find(&logs).Error // todo add retry

	return logs, err
}
