package wallets

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/tee-node/pkg/wallets"
)

// KeyData holds the key existence record and its cryptographic existence proof.
type KeyData struct {
	Info  KeyExistence                     `json:"info"`
	Proof *wallets.SignedKeyExistenceProof `json:"proof"`
}

// KeyExistence contains the identifying attributes and public key of a wallet key.
type KeyExistence struct {
	TeeID           common.Address  `json:"teeId"`
	WalletID        common.Hash     `json:"walletId"`
	KeyID           uint64          `json:"keyId"`
	KeyType         common.Hash     `json:"keyType"`
	SigningAlgo     common.Hash     `json:"signingAlgo"`
	PublicKey       hexutil.Bytes   `json:"publicKey"`
	Nonce           *big.Int        `json:"nonce"`
	Restored        bool            `json:"restore"`
	ConfigConstants ConfigConstants `json:"configConstants"`
	SettingsVersion common.Hash     `json:"settingsVersion"`
	Settings        hexutil.Bytes   `json:"settings"`
}

// PublicKey holds the X and Y coordinates of an elliptic-curve public key.
type PublicKey struct {
	X common.Hash `json:"x"`
	Y common.Hash `json:"y"`
}

// ConfigConstants holds the immutable wallet configuration such as admin keys and cosigners.
type ConfigConstants struct {
	AdminsPublicKeys   []PublicKey      `json:"adminsPublicKeys"`
	AdminsThreshold    uint64           `json:"adminsThreshold"`
	Cosigners          []common.Address `json:"cosigners"`
	CosignersThreshold uint64           `json:"cosignersThreshold"`
}

// ConfigSettings holds the mutable wallet settings such as pausing addresses and op-type settings.
type ConfigSettings struct {
	PausingAddresses []common.Address `json:"pausingAddresses"`
	OPTypeSettings   []byte           `json:"opTypeSettings"`
}
