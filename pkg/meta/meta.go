package meta

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/constants"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs"
	"github.com/flare-foundation/go-flare-common/pkg/tee/structs/connector"

	"github.com/flare-foundation/tee-proxy/pkg/wallets"

	"github.com/flare-foundation/tee-node/pkg/types"
)

// Todo join into one function to avoid double lookup

// Meta provides meta data for the instructions.
type Meta interface {
	// Cosigners returns cosigners' addresses and the cosigners threshold.
	//
	// If no cosigners are set, empty list and threshold zero is returned.
	Cosigners(*instruction.DataFixed) (map[common.Address]bool, uint64, error)
	// Threshold returns custom threshold for the instruction.
	//
	// If no specific threshold is set -1 is returned.
	Threshold(*instruction.DataFixed) (int, error)
}

type meta struct {
	ws *wallets.Storage
}

func New(ws *wallets.Storage) Meta {
	return &meta{ws}
}

func (m *meta) Cosigners(data *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	cosigners := make(map[common.Address]bool)

	//  todo add reissue and other commands
	if data.OPType == constants.XRP.Hash() && (data.OPCommand == constants.Pay.Hash() || data.OPCommand == constants.Reissue.Hash()) { //OPType=="XRP", OPCommand == "PAY" or "REISSUE"
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
		var message = new(connector.IFtdcHubFtdcProve)
		err := structs.DecodeTo(connector.MessageArguments[constants.Prove], data.OriginalMessage, message)
		if err != nil {
			return nil, 0, err
		}

		for _, cs := range message.Cosigners {
			cosigners[cs] = true
		}

		return cosigners, message.CosignersThreshold, nil
	}

	return cosigners, 0, nil
}

func (m *meta) Threshold(data *instruction.DataFixed) (int, error) {
	if data.OPType == constants.FTDC.Hash() && data.OPCommand == constants.Prove.Hash() { // OPType == "FTDC", OPCommand == "PROVE"
		var message = new(connector.IFtdcHubFtdcProve)
		err := structs.DecodeTo(connector.MessageArguments[constants.Prove], data.OriginalMessage, message)
		if err != nil {
			return -1, err
		}

		return int(message.ThresholdBIPS), nil // todo: is this always set??
	}

	return -1, nil
}
