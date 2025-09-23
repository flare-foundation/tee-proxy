package wallets

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

type KeyData struct {
	Info  KeyExistence                     `json:"info"`
	Proof *wallets.SignedKeyExistenceProof `json:"proof"`
}

type KeyExistence struct {
	TeeID           common.Address  `json:"teeId"`
	WalletID        common.Hash     `json:"walletId"`
	KeyID           uint64          `json:"keyId"`
	KeyType         common.Hash     `json:"keyType"`
	SigningAlgo     common.Hash     `json:"signingAlgo"`
	PublicKey       hexutil.Bytes   `json:"publicKye"`
	Nonce           *big.Int        `json:"nonce"`
	Restored        bool            `json:"restore"`
	ConfigConstants ConfigConstants `json:"configConstants"`
	SettingsVersion common.Hash     `json:"settingsVersion"`
	Settings        hexutil.Bytes   `json:"settings"`
}

type PublicKey struct {
	X common.Hash `json:"x"`
	Y common.Hash `json:"y"`
}

type ConfigConstants struct {
	AdminsPublicKeys   []PublicKey      `json:"adminsPublicKeys"`
	AdminsThreshold    uint64           `json:"adminsThreshold"`
	Cosigners          []common.Address `json:"cosigners"`
	CosignersThreshold uint64           `json:"cosignersThreshold"`
}

type ConfigSettings struct {
	PausingAddresses []common.Address `json:"pausingAddresses"`
	OPTypeSettings   []byte           `json:"opTypeSettings"`
}

func (s *Storage) KeyData(walletID common.Hash, keyID uint64) (*KeyData, error) {
	s.RLock()
	defer s.RUnlock()

	id := IDPair{WalletID: walletID, KeyID: keyID}
	info, exists := s.Keys[id]
	if !exists {
		return nil, fmt.Errorf("%w: key data not found", status.HTTP[404])
	}

	return info, nil
}
