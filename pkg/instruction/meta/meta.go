package meta

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"

	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/status"

	"github.com/flare-foundation/tee-node/pkg/fdc"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-node/pkg/utils"
	nodewallets "github.com/flare-foundation/tee-node/pkg/wallets"
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
	// A declared list naming the same address twice is rejected.
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
	// Set equality below is containment plus equal length, which a repeated address defeats.
	// The node rejects duplicates outright, so agree here rather than burn a voting round.
	if utils.HasDuplicateAddresses(data.Cosigners) {
		return nil, 0, ErrDuplicateCosigners
	}

	var cosigners map[common.Address]bool
	var threshold uint64
	var err error

	switch data.OPCommand {
	case op.Pay.Hash(), op.Reissue.Hash():
		cosigners, threshold, err = xrpCosigners(data, m.ws)
	case op.VRF.Hash():
		cosigners, threshold, err = vrfCosigners(data, m.ws)
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

// Exported so voting.RejectReason can classify rejections into bounded metric labels.
var (
	// ErrCosignerMismatch reports a client-declared cosigner set that does not match the authoritative wallet/admin configuration.
	ErrCosignerMismatch = fmt.Errorf("%w: invalid cosigners", status.HTTP[400])
	// ErrCosignerThresholdMismatch reports a client-declared cosigner threshold that does not match the authoritative configuration.
	ErrCosignerThresholdMismatch = fmt.Errorf("%w: invalid cosigner threshold", status.HTTP[400])
	// ErrDuplicateCosigners reports a declared cosigner list naming the same address more than once.
	ErrDuplicateCosigners = fmt.Errorf("%w: duplicate cosigners", status.HTTP[400])
	// ErrInvalidBackupMetadata reports backup metadata failing an integrity rule the node enforces on restore.
	ErrInvalidBackupMetadata = fmt.Errorf("%w: invalid backup metadata", status.HTTP[400])
	// ErrMalformedPayload reports an unparseable cosigner-resolution payload.
	ErrMalformedPayload = fmt.Errorf("%w: malformed payload", status.HTTP[400])

	// ErrFDCThresholdTooLow reports an FDC2 PROVE data-provider threshold below the minimum the TEE accepts.
	ErrFDCThresholdTooLow = fmt.Errorf("%w: FDC2 data provider threshold below minimum", status.HTTP[400])
	// ErrFDCThresholdBelowHalf reports a below-half FDC2 PROVE threshold lacking the required cosigner majority.
	ErrFDCThresholdBelowHalf = fmt.Errorf("%w: FDC2 data provider threshold below half requires cosigner threshold above half", status.HTTP[400])
	// ErrFDCThresholdTooHigh reports an FDC2 PROVE data-provider threshold at or above the maximum.
	ErrFDCThresholdTooHigh = fmt.Errorf("%w: FDC2 data provider threshold too high", status.HTTP[400])
)

func checkCosigner(cosigners []common.Address, expectedCosigners map[common.Address]bool, threshold, expectedThreshold uint64) error {
	if len(cosigners) != len(expectedCosigners) {
		return ErrCosignerMismatch
	}

	for _, cs := range cosigners {
		if !expectedCosigners[cs] {
			return ErrCosignerMismatch
		}
	}

	if threshold != expectedThreshold {
		return ErrCosignerThresholdMismatch
	}

	return nil
}

// keyDataProviderRestoreAdmins resolves the restore cosigner roster from backup metadata.
//
// The metadata is unauthenticated beyond its backup ID, and that ID does not cover the
// rosters, so the node revalidates it field by field in keyRestoreDataCheck. Those checks
// are mirrored here to reject before a restore burns its whole voting window.
func keyDataProviderRestoreAdmins(data *instruction.DataFixed) (map[common.Address]bool, uint64, error) {
	var walletBackupMetadata backup.WalletBackupMetaData
	err := json.Unmarshal(data.AdditionalFixedMessage, &walletBackupMetadata)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: unmarshaling backup metadata: %v", ErrMalformedPayload, err)
	}

	err = nodewallets.ValidateWalletMemberCounts(len(walletBackupMetadata.AdminsPublicKeys), len(walletBackupMetadata.Cosigners))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrInvalidBackupMetadata, err)
	}

	adminAddresses, err := utils.PubKeysToAddresses(walletBackupMetadata.AdminsPublicKeys)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parsing admin public key: %v", ErrMalformedPayload, err)
	}

	if utils.HasDuplicateAddresses(adminAddresses) {
		return nil, 0, fmt.Errorf("%w: duplicate admin addresses", ErrInvalidBackupMetadata)
	}

	if utils.HasDuplicateAddresses(walletBackupMetadata.Cosigners) {
		return nil, 0, fmt.Errorf("%w: duplicate cosigner addresses", ErrInvalidBackupMetadata)
	}

	if walletBackupMetadata.CosignersThreshold > uint64(len(walletBackupMetadata.Cosigners)) {
		return nil, 0, fmt.Errorf("%w: cosigners threshold exceeds cosigner set", ErrInvalidBackupMetadata)
	}

	cosigners := make(map[common.Address]bool, len(adminAddresses))
	for _, a := range adminAddresses {
		cosigners[a] = true
	}

	return cosigners, walletBackupMetadata.AdminsThreshold, nil
}

// xrpCosigners retrieves cosigners for payment instruction from wallets configurations.
func xrpCosigners(data *instruction.DataFixed, ws *wallets.Service) (map[common.Address]bool, uint64, error) {
	originalMessage, err := types.ParsePaymentInstruction(data)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parsing payment instruction: %v", ErrMalformedPayload, err)
	}

	return walletCosigners(originalMessage.WalletId, ws)
}

// vrfCosigners retrieves cosigners for a VRF instruction from wallets configurations.
//
// The node checks the same roster off the named wallet key, so a client-declared set is
// never authoritative here.
func vrfCosigners(data *instruction.DataFixed, ws *wallets.Service) (map[common.Address]bool, uint64, error) {
	originalMessage, err := types.ParseVRFInstruction(data)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parsing VRF instruction: %v", ErrMalformedPayload, err)
	}

	return walletCosigners(originalMessage.WalletId, ws)
}

// walletCosigners reads the cosigner roster and threshold off the wallet configuration.
func walletCosigners(walletID common.Hash, ws *wallets.Service) (map[common.Address]bool, uint64, error) {
	wi, err := ws.WalletInfo(walletID)
	if err != nil {
		return nil, 0, err
	}

	cosigners := make(map[common.Address]bool, len(wi.ConfigConstants.Cosigners))
	for _, cs := range wi.ConfigConstants.Cosigners {
		cosigners[cs] = true
	}

	return cosigners, wi.ConfigConstants.CosignersThreshold, nil
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
// Data providers and cosigners sign the chain-bound Relay Mode-2 prefixed hash
// (matches the on-chain Fdc2ProofVerification.toCosignersMessageHash +
// Relay.relay() recovery path); see fdc.ChainBoundRelayPrefixedHash. Verifying
// against the bare messageHash would fail every provider signature with
// "signature check fail".
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
	dpSigningHash := fdc.ChainBoundRelayPrefixedHash(chainID, h)

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
		return ErrFDCThresholdTooLow
	case thresholdBIPS < maxBIPS/2 && cosignersThreshold*2 <= uint64(cosignerCount):
		return ErrFDCThresholdBelowHalf
	case thresholdBIPS >= maxBIPS:
		return ErrFDCThresholdTooHigh
	}

	return nil
}
