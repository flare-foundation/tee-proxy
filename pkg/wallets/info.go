package wallets

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/tee-node/pkg/wallets"
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
