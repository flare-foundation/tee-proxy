package meta

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"

	"github.com/flare-foundation/tee-proxy/pkg/wallets"

	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
)

// Todo join into one function to avoid double lookup

// Meta provides meta data for the instructions.
type Meta interface {
	// Cosigners returns cosigners' addresses and the cosigners threshold.
	//
	// If no cosigners are set, empty list and threshold zero is returned.
	Cosigners(*instruction.DataFixed) (map[common.Address]bool, uint64, error)

	// CheckConsistency validates specific OPCommands with additional logic as needed.
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
	cosigners := make(map[common.Address]bool)

	if data.OPType == constants.XRP.Hash() && (data.OPCommand == constants.Pay.Hash() || data.OPCommand == constants.Reissue.Hash()) { // OPType=="XRP", OPCommand == "PAY" or "REISSUE"
		originalMessage, err := types.ParseSignPaymentRequest(data)
		if err != nil {
			return nil, 0, err
		}

		wID := originalMessage.WalletId

		wi, err := m.ws.WalletInfo(wID)
		if err != nil {
			return nil, 0, err
		}

		for _, cs := range wi.ConfigConstants.Cosigners {
			cosigners[cs] = true
		}

		cosignerThreshold := wi.ConfigConstants.CosignersThreshold

		return cosigners, cosignerThreshold, nil
	}
	if data.OPType == constants.FTDC.Hash() && data.OPCommand == constants.Prove.Hash() { // OPType == "FTDC", OPCommand == "PROVE"
		ftdcReq, err := types.DecodeFTDCRequest(data.OriginalMessage)
		if err != nil {
			return nil, 0, err
		}

		for _, cs := range ftdcReq.Header.Cosigners {
			cosigners[cs] = true
		}

		return cosigners, ftdcReq.Header.CosignersThreshold, nil
	}

	return cosigners, 0, nil
}

func (m *meta) CheckConsistency(data *instruction.Data, signer common.Address) error {
	if data.OPType == constants.FTDC.Hash() && data.OPCommand == constants.Prove.Hash() {
		fdcReq, err := types.DecodeFTDCRequest(data.OriginalMessage)
		if err != nil {
			return err
		}

		resBody := data.AdditionalFixedMessage
		h, _, err := types.HashFTDCMessage(fdcReq, resBody, uint64(data.Timestamp))
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

	return nil
}

func (m *meta) ThresholdBIPS(data *instruction.DataFixed) (int, error) {
	if data.OPType == constants.FTDC.Hash() && data.OPCommand == constants.Prove.Hash() { // OPType == "FTDC", OPCommand == "PROVE"
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
