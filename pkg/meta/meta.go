package meta

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"

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
	Cosigners(*instruction.DataFixed) (map[common.Address]bool, uint64, error)

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

func (m *meta) Cosigners(data *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	switch data.OPCommand {
	case constants.Pay.Hash(), constants.Reissue.Hash():
		return xrpCosigners(data, m.ws)

	case constants.Prove.Hash():
		return ftdcCosigners(data)

	case constants.KeyDataProviderRestore.Hash():
		return keyDataProviderRestoreAdmins(data)
	}

	return make(map[common.Address]bool), 0, nil
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

// xrpCosigners retrieves cosigners for payment instruction from wallets configurations.
func xrpCosigners(data *instruction.DataFixed, ws *wallets.Storage) (map[common.Address]bool, uint64, error) {
	cosigners := make(map[common.Address]bool)

	originalMessage, err := types.ParsePaymentInstruction(data)
	if err != nil {
		return nil, 0, err
	}

	wID := originalMessage.WalletId

	wi, err := ws.WalletInfo(wID)
	if err != nil {
		return nil, 0, err
	}

	for _, cs := range wi.ConfigConstants.Cosigners {
		cosigners[cs] = true
	}

	cosignerThreshold := wi.ConfigConstants.CosignersThreshold

	return cosigners, cosignerThreshold, nil
}

// ftdcCosigners retrieves cosigners and threshold from the header of the FTDC request.
func ftdcCosigners(data *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	cosigners := make(map[common.Address]bool)

	ftdcReq, err := types.DecodeFTDCRequest(data.OriginalMessage)
	if err != nil {
		return nil, 0, err
	}

	for _, cs := range ftdcReq.Header.Cosigners {
		cosigners[cs] = true
	}

	return cosigners, ftdcReq.Header.CosignersThreshold, nil
}

func (*meta) CheckConsistency(data *instruction.Data, signer common.Address) error {
	switch data.OPCommand {
	case constants.Prove.Hash():
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
	h, _, err := types.HashFTDCMessage(ftdcReq, resBody, data.Timestamp)
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
	if data.OPType == constants.FTDC.Hash() && data.OPCommand == constants.Prove.Hash() { // OPType == "F_FTDC", OPCommand == "PROVE"
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
