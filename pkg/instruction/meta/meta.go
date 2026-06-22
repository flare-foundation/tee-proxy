package meta

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/flare-foundation/tee-node/pkg/fdc"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/flare-foundation/tee-node/pkg/wallets/backup"
)

const (
	maxBIPS = 10000

	// fdcMinimumThresholdBIPS is the lowest non-zero FDC2 PROVE data-provider
	// threshold (in BIPS) the TEE accepts.
	fdcMinimumThresholdBIPS = 4000
)

// Meta provides meta data for the instructions.
type Meta interface {
	// Cosigners returns cosigners' addresses and the cosigners threshold.
	//
	// If no cosigners are set, empty set and threshold zero is returned.
	Cosigners(*instruction.DataFixed) (map[common.Address]bool, uint64, error)

	// CheckConsistency validates instruction according to its opType.
	//
	// For example, for the F_FDC2 Prove OPCommand, it verifies the internal signature of the FDC message.
	CheckConsistency(*instruction.Data, common.Address) error

	// ThresholdBIPS returns custom thresholdBIPS for the instruction.
	// If no specific threshold is set -1 is returned.
	ThresholdBIPS(*instruction.DataFixed) (int, error)
}

type meta struct {
	ws      *wallets.Service
	chainID uint64
}

// New creates a Meta implementation backed by the given wallets Service.
// chainID is bound into FDC2 message hashes during consistency checks; it
// must match the chainID configured on the TEE node.
func New(ws *wallets.Service, chainID uint64) Meta {
	return &meta{ws: ws, chainID: chainID}
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

var (
	errInvalidCosigners         = errors.New("invalid cosigners")
	errInvalidCosignerThreshold = errors.New("invalid cosigner threshold")

	errFDCThresholdTooLow    = fmt.Errorf("%w: FDC2 data provider threshold below minimum", status.HTTP[400])
	errFDCThresholdBelowHalf = fmt.Errorf("%w: FDC2 data provider threshold below half requires cosigner threshold above half", status.HTTP[400])
	errFDCThresholdTooHigh   = fmt.Errorf("%w: FDC2 data provider threshold too high", status.HTTP[400])
)

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
		return errInvalidCosignerThreshold
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
func xrpCosigners(data *instruction.DataFixed, ws *wallets.Service) (map[common.Address]bool, uint64, error) {
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

func (m *meta) CheckConsistency(data *instruction.Data, signer common.Address) error {
	switch data.OPCommand {
	case op.Prove.Hash():
		return fdcCheckConsistency(m.chainID, data, signer)
	}

	return nil
}

// fdcCheckConsistency checks that signer of the FDC message is the same as the signer of the whole instruction.
//
// Data providers and cosigners sign the Relay Mode-2 prefixed hash (matches the
// on-chain Verification.toCosignersMessageHash + Relay.relay() recovery path);
// see fdc.RelayPrefixedHash. Verifying against the bare messageHash would fail
// every provider signature with "signature check fail".
func fdcCheckConsistency(chainID uint64, data *instruction.Data, signer common.Address) error {
	fdcReq, err := fdc.DecodeRequest(data.OriginalMessage)
	if err != nil {
		return fmt.Errorf("decoding FDC request: %w", err)
	}

	resBody := data.AdditionalFixedMessage
	h, _, err := fdc.HashMessage(chainID, fdcReq, resBody, data.Cosigners, data.CosignersThreshold, data.Timestamp)
	if err != nil {
		return fmt.Errorf("hashing FDC message: %w", err)
	}
	dpSigningHash := fdc.RelayPrefixedHash(h)

	sig := data.AdditionalVariableMessage
	err = utils.VerifySignature(dpSigningHash[:], sig, signer)
	if err != nil {
		return fmt.Errorf("verifying FDC signature: %w", err)
	}

	return nil
}

func (*meta) ThresholdBIPS(data *instruction.DataFixed) (int, error) {
	if data.OPType == op.FDC2.Hash() && data.OPCommand == op.Prove.Hash() { // OPType == "F_FDC2", OPCommand == "PROVE"
		fdcReq, err := fdc.DecodeRequest(data.OriginalMessage)
		if err != nil {
			return -1, err
		}

		tBIPS := fdcReq.Header.ThresholdBIPS
		if tBIPS == 0 {
			return -1, nil
		}

		if err := checkFDCThreshold(tBIPS, data.CosignersThreshold, len(data.Cosigners)); err != nil {
			return -1, err
		}

		return int(tBIPS), nil
	}

	return -1, nil
}

// checkFDCThreshold validates a non-zero FDC2 PROVE data-provider threshold against the
// rules the TEE enforces: it must be at least
// fdcMinimumThresholdBIPS, strictly below maxBIPS, and — when below half — paired with a
// cosigner threshold above half of the cosigners. Mirroring the rules keeps the proxy
// from opening, finalizing, or signing a receipt for a vote the TEE would reject.
func checkFDCThreshold(thresholdBIPS uint16, cosignersThreshold uint64, cosignerCount int) error {
	switch {
	case thresholdBIPS < fdcMinimumThresholdBIPS:
		return errFDCThresholdTooLow
	case thresholdBIPS < maxBIPS/2 && cosignersThreshold*2 <= uint64(cosignerCount):
		return errFDCThresholdBelowHalf
	case thresholdBIPS >= maxBIPS:
		return errFDCThresholdTooHigh
	}

	return nil
}
