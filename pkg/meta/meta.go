package meta

import (
	"encoding/json"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-proxy/pkg/wallets"

	"github.com/flare-foundation/tee-node/pkg/ftdc"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-node/pkg/wallets/backup"
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
	var cosigners map[common.Address]bool
	var threshold uint64
	var err error

	switch data.OPCommand {
	case op.Pay.Hash(), op.Reissue.Hash():
		cosigners, threshold, err = xrpCosigners(data, m.ws)
	case op.KeyDataProviderRestore.Hash():
		cosigners, threshold, err = keyDataProviderRestoreAdmins(data)

	default:
		cosigners = make(map[common.Address]bool, len(data.Cosigners))

		for _, cs := range data.Cosigners {
			cosigners[cs] = true
		}

		return cosigners, data.CosignersThreshold, nil
	}

	if err != nil {
		return nil, 0, err
	}

	err = checkCosigner(data.Cosigners, cosigners, data.CosignersThreshold, threshold)
	if err != nil {
		return nil, 0, err
	}

	return cosigners, threshold, nil
}

var errInvalidCosigners = errors.New("invalid cosigners")

func checkCosigner(cosigners []common.Address, expectedCosigners map[common.Address]bool, threshold, expectedThreshold uint64) error {
	if len(cosigners) != len(expectedCosigners) {
		return errInvalidCosigners
	}

	for _, cs := range cosigners {
		if !expectedCosigners[cs] {
			return errInvalidCosigners
		}
	}

	if threshold != expectedThreshold {
		return errors.New("invalid cosigner threshold")
	}

	return nil
}

func keyDataProviderRestoreAdmins(data *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	cosigners := make(map[common.Address]bool)

	var walletBackupMetadata backup.WalletBackupMetaData
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

func (*meta) CheckConsistency(data *instruction.Data, signer common.Address) error {
	switch data.OPCommand {
	case op.Prove.Hash():
		return ftdcCheckConsistency(data, signer)
	}

	return nil
}

// ftdcCheckConsistency checks that signer of the ftdc message is the same as the signer of the whole instruction.
func ftdcCheckConsistency(data *instruction.Data, signer common.Address) error {
	ftdcReq, err := ftdc.DecodeRequest(data.OriginalMessage)
	if err != nil {
		return err
	}

	resBody := data.AdditionalFixedMessage
	h, _, _, err := ftdc.HashMessage(ftdcReq, resBody, data.Cosigners, data.CosignersThreshold, data.Timestamp)
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
		ftdcReq, err := ftdc.DecodeRequest(data.OriginalMessage)
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
