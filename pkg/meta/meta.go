package meta

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-proxy/pkg/wallets"

	"github.com/flare-foundation/tee-node/pkg/backup"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
)

// Meta provides meta data for the instructions.
type Meta interface {
	// Cosigners returns cosigners' addresses and the cosigners threshold.
	//
	// If no cosigners are set, empty list and threshold zero is returned.
	Cosigners(*instruction.DataFixed) (map[common.Address]bool, uint64)

	// CheckConsistency validates validates instruction according to its opType.
	//
	// For example, for the FTDC Prove OPCommand, it verifies the internal signature of the FTDC message.
	CheckConsistency(*instruction.Data, common.Address) error

	// ThresholdBIPS returns custom thresholdBIPS for the instruction.
	// If no specific threshold is set -1 is returned.
	ThresholdBIPS(*instruction.DataFixed) (int, error)
}

type meta struct {
	ws *wallets.Storage
}

func New(ws *wallets.Storage) Meta {
	return &meta{ws}
}

func (m *meta) Cosigners(data *instruction.DataFixed) (map[common.Address]bool, uint64) {
	cosigners := make(map[common.Address]bool)

	for _, cs := range data.Cosigners {
		cosigners[cs] = true
	}

	return cosigners, data.CosignersThreshold
}

func keyDataProviderRestoreAdmins(data *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	cosigners := make(map[common.Address]bool)

	var walletBackupMetadata backup.WalletBackupMetaData // Note: Is this correct?
	err := json.Unmarshal(data.AdditionalFixedMessage, &walletBackupMetadata)
	if err != nil {
		return nil, 0, err
	}

	for _, admin := range walletBackupMetadata.AdminsPublicKeys {
		adminPub, err := types.ParsePubKey(admin)
		if err != nil {
			return nil, 0, err
		}
		cosigners[crypto.PubkeyToAddress(*adminPub)] = true
	}

	return cosigners, walletBackupMetadata.AdminsThreshold, nil
}

func (*meta) CheckConsistency(data *instruction.Data, signer common.Address) error {
	switch data.OPCommand {
	case op.Prove.Hash():
		return ftdcCheckConsistency(data, signer)
	}

	return nil
}

// ftdcCheckConsistency checks that signer of the ftdc message is the same as the signer of the whole instruction.
func ftdcCheckConsistency(data *instruction.Data, signer common.Address) error {
	ftdcReq, err := types.DecodeFTDCRequest(data.OriginalMessage)
	if err != nil {
		return err
	}

	resBody := data.AdditionalFixedMessage
	h, _, _, err := types.HashFTDCMessage(ftdcReq, resBody, data.Cosigners, data.CosignersThreshold, data.Timestamp)
	if err != nil {
		return err
	}

	sig := data.AdditionalVariableMessage
	err = utils.VerifySignature(h[:], sig, signer)
	if err != nil {
		return err
	}

	return nil
}

func (*meta) ThresholdBIPS(data *instruction.DataFixed) (int, error) {
	if data.OPType == op.FTDC.Hash() && data.OPCommand == op.Prove.Hash() { // OPType == "F_FTDC", OPCommand == "PROVE"
		ftdcReq, err := types.DecodeFTDCRequest(data.OriginalMessage)
		if err != nil {
			return -1, err
		}

		tBIPS := int(ftdcReq.Header.ThresholdBIPS)
		if tBIPS == 0 {
			return -1, nil
		}

		return int(ftdcReq.Header.ThresholdBIPS), nil
	}

	return -1, nil
}
