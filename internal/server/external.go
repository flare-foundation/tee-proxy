package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"
	cinstruction "github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/info"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

const (
	actionID      = "actionID"
	instructionID = "instructionID"
	keyID         = "keyID"
	rewardEpochID = "rewardEpochID"
	walletID      = "walletID"
	backupIDHash  = "backupIDHash"

	mib = 1 << 20
	kib = 1 << 10

	instructionSizeLimit = 10 * mib
	maxHeaderBytes       = 16 * kib
)

var (
	errNoInfoService = errors.New("nil info service")

	errProxyNotInitialized  = fmt.Errorf("%w: proxy not initialized", status.HTTP[http.StatusServiceUnavailable])
	errEmptySubmissionTag   = fmt.Errorf("%w: empty submission tag query string", status.HTTP[http.StatusBadRequest])
	errInvalidSubmissionTag = fmt.Errorf("%w: invalid submission tag (end, threshold, or submit)", status.HTTP[http.StatusBadRequest])
	errSystemDirect         = fmt.Errorf("%w: system op types not allowed in external direct requests", status.HTTP[http.StatusBadRequest])
	errUnauthorized         = fmt.Errorf("%w: unauthorized", status.HTTP[http.StatusUnauthorized])
)

// External is the client-facing HTTP server exposing instruction, result, wallet, and TEE info endpoints.
type External struct {
	server *http.Server

	instructionService *instruction.Service
	actionQueues       *queue.ActionQueues
	resultService      ResultService
	teeInfo            *info.Service
	wallet             *wallets.Service

	privKey *ecdsa.PrivateKey
	chainID uint64
	direct  DirectConfig

	metrics *metrics.Metrics
}

// DirectConfig holds configuration for the /direct endpoint.
type DirectConfig struct {
	Enable         bool
	APIKey         string
	APIKeyOptional bool
	MaxBodySize    int64
}

// NewExternal creates and configures a new External server listening on port.
func NewExternal(
	port string,
	instructionService *instruction.Service,
	resultService ResultService,
	teeInfo *info.Service,
	wallet *wallets.Service,
	privateKey *ecdsa.PrivateKey,
	chainID uint64,
	actionQueues *queue.ActionQueues,
	direct DirectConfig,
	m *metrics.Metrics,
) *External {
	addr := fmt.Sprintf(":%s", port)

	server := &http.Server{
		Addr:              addr,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	e := External{
		instructionService: instructionService,
		actionQueues:       actionQueues,
		resultService:      resultService,
		server:             server,
		teeInfo:            teeInfo,
		wallet:             wallet,
		privKey:            privateKey,
		chainID:            chainID,
		direct:             direct,
		metrics:            m,
	}

	e.registerRoutes(direct.Enable)

	return &e
}

// Serve starts the server.
func (e *External) Serve() error {
	logger.Infof("serving external at %s", e.server.Addr)
	return e.server.ListenAndServe()
}

// Close gracefully closes the server.
func (e *External) Close(ctx context.Context) error {
	return e.server.Shutdown(ctx)
}

// registerRoutes adds routs to the server.
// With enableDirect set to true /direct endpoint is added.
func (e *External) registerRoutes(enableDirect bool) {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /instruction", prepareHandler(e.instructionH, instructionSizeLimit, false))
	mux.HandleFunc(fmt.Sprintf("GET /action/result/{%s}", actionID), prepareHandler(e.resultH, noBody, false))
	mux.HandleFunc(fmt.Sprintf("GET /action/status/{%s}/{%s}", rewardEpochID, instructionID), prepareHandler(e.statusH, noBody, false))
	mux.HandleFunc("GET /info", prepareHandler(e.infoH, noBody, false))
	mux.HandleFunc("GET /wallet/info", prepareHandler(e.walletInfoH, noBody, false))
	mux.HandleFunc(fmt.Sprintf("GET /wallet/{%s}/{%s}", walletID, keyID), prepareHandler(e.walletH, noBody, false))
	mux.HandleFunc(fmt.Sprintf("GET /backup/{%s}", backupIDHash), prepareHandler(e.backupH, noBody, false))
	mux.HandleFunc(fmt.Sprintf("GET /backup/{%s}/{%s}", walletID, keyID), prepareHandler(e.backupLatestH, noBody, false))

	if enableDirect {
		sizeLimit := int64(instructionSizeLimit)
		if e.direct.MaxBodySize > 0 {
			sizeLimit = e.direct.MaxBodySize
		}

		mux.HandleFunc("POST /direct", prepareHandler(e.directH, sizeLimit, false))
	}

	e.server.Handler = instrumentHTTP(e.metrics, "external", mux)
}

// instructionH handles instruction endpoint.
// It adds instruction to the voting process and returns a signed receipt.
func (e *External) instructionH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	i := new(cinstruction.Instruction)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&i)
	if err != nil {
		return ErrInvalidBody
	}

	receipt, err := e.instructionService.ServeInstruction(ctx, i)
	if err != nil {
		return err
	}

	signedReceipt, err := receipt.Sign(e.privKey, e.chainID)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(signedReceipt)
	if err != nil {
		return err
	}

	return nil
}

// verifyAPIKey checks that the request contains a valid X-API-Key header.
func (e *External) verifyAPIKey(r *http.Request) error {
	key := r.Header.Get("X-API-Key")
	if subtle.ConstantTimeCompare([]byte(key), []byte(e.direct.APIKey)) != 1 {
		return errUnauthorized
	}

	return nil
}

// directH handles direct endpoint.
// The request should provide direct instruction.
// A direct action and put into queue and also returned to the caller.
func (e *External) directH(w http.ResponseWriter, r *http.Request) error {
	if !e.direct.APIKeyOptional {
		err := e.verifyAPIKey(r)
		if err != nil {
			return err
		}
	}

	i := new(types.DirectInstruction)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&i)
	if err != nil {
		return ErrInvalidBody
	}

	err = validateDirect(i)
	if err != nil {
		return err
	}

	a, err := queue.DirectInstructionToAction(i)
	if err != nil {
		return ErrInvalidBody
	}

	err = e.actionQueues.Enqueue(r.Context(), a, processorutils.Direct)
	if err != nil {
		return ErrInvalidBody
	}

	err = json.NewEncoder(w).Encode(a)
	if err != nil {
		return err
	}

	return nil
}

// validateDirect checks that the instruction does not have system op Type.
func validateDirect(i *types.DirectInstruction) error {
	if op.HashToOPType(i.OPType).IsSystem() {
		return errSystemDirect
	}

	return nil
}

// resultH handles servers result/{actionID} endpoint.
// A "submissionTag" query parameter is optional. It defaults to "threshold".
// It serves response with the given actionID and submissionTag if present.
func (e *External) resultH(w http.ResponseWriter, r *http.Request) error {
	err := r.ParseForm()
	if err != nil {
		return fmt.Errorf("%w: %v", status.HTTP[400], err)
	}

	id, err := hashParam(r, actionID)
	if err != nil {
		return err
	}

	st, err := submissionTagParam(r)
	if err != nil {
		return err
	}

	response, err := e.resultService.Serve(r.Context(), id, st)
	if err != nil {
		return err
	}

	resultSignHash, err := csigning.NewPayload(csigning.ProxyActionResult, e.chainID, common.BytesToHash(response.Result.Hash())).Hash()
	if err != nil {
		return err
	}
	response.ProxySignature, err = crypto.Sign(accounts.TextHash(resultSignHash[:]), e.privKey)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(response)
	if err != nil {
		return err
	}

	return nil
}

// statusH serves status/{instructionID} endpoint.
// It returns statuses of voting for instructionID.
func (e *External) statusH(w http.ResponseWriter, r *http.Request) error {
	id, err := hashParam(r, instructionID)
	if err != nil {
		return err
	}

	reID, err := uint32Param(r, rewardEpochID)
	if err != nil {
		return err
	}

	result, err := e.instructionService.Statuses(id, reID)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		return err
	}

	return nil
}

// infoH serves info endpoint.
// It returns latest signed tee info.
func (e *External) infoH(w http.ResponseWriter, r *http.Request) error {
	if e.teeInfo == nil {
		return errNoInfoService
	}

	e.teeInfo.RLock()
	defer e.teeInfo.RUnlock()

	result := e.teeInfo.Latest

	if result == nil {
		return errProxyNotInitialized
	}

	h, err := result.TeeInfo.Hash()
	if err != nil {
		return err
	}

	// Bind with the chainID carried inside the attestation itself so the
	// recovering client (which only has the transmitted TeeInfo) reconstructs
	// the same preimage without relying on a separately-configured chainID.
	infoSignHash, err := csigning.NewPayload(csigning.ProxyTeeInfo, result.TeeInfo.ChainID, common.BytesToHash(h)).Hash()
	if err != nil {
		return err
	}

	sig, err := crypto.Sign(accounts.TextHash(infoSignHash[:]), e.privKey)
	if err != nil {
		return err
	}

	info := types.SignedTeeInfoResponse{
		TeeInfoResponse: *result,
		ProxySignature:  sig,
	}

	err = json.NewEncoder(w).Encode(info)
	if err != nil {
		return err
	}

	return nil
}

// walletInfoH serves wallet/info endpoint.
// It returns summary information about all stored wallets and keys.
func (e *External) walletInfoH(w http.ResponseWriter, _ *http.Request) error {
	return json.NewEncoder(w).Encode(e.wallet.WalletsInfo())
}

// walletH serves wallet/{walletID}/{keyID} endpoint.
// It returns key data for the indicated key if present.
func (e *External) walletH(w http.ResponseWriter, r *http.Request) error {
	wID, err := hashParam(r, walletID)
	if err != nil {
		return err
	}

	kID, err := uint64Param(r, keyID)
	if err != nil {
		return err
	}

	walletInfo, err := e.wallet.KeyData(wID, kID)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(walletInfo)
	if err != nil {
		return err
	}
	return nil
}

// backupH serves backup/{backupIDHash} endpoint.
// It returns backup data for the given backup ID hash if present.
func (e *External) backupH(w http.ResponseWriter, r *http.Request) error {
	idHash, err := hashParam(r, backupIDHash)
	if err != nil {
		return err
	}

	backupResponse, err := e.wallet.FetchBackup(r.Context(), idHash)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(backupResponse)
	if err != nil {
		return err
	}
	return nil
}

// backupLatestH serves backup/{walletID}/{keyID} endpoint.
// It returns the latest backup data for the given wallet ID and key ID if present.
func (e *External) backupLatestH(w http.ResponseWriter, r *http.Request) error {
	wID, err := hashParam(r, walletID)
	if err != nil {
		return err
	}

	kID, err := uint64Param(r, keyID)
	if err != nil {
		return err
	}

	backupResponse, err := e.wallet.FetchLatestBackup(r.Context(), wallets.IDPair{
		WalletID: wID,
		KeyID:    kID,
	})
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(backupResponse)
	if err != nil {
		return err
	}
	return nil
}

// submissionTagParam reads "submissionTag" query parameter.
func submissionTagParam(r *http.Request) (types.SubmissionTag, error) {
	submissionTags, exists := r.Form["submissionTag"]
	if !exists {
		return types.Threshold, nil
	}

	if len(submissionTags) != 1 {
		return types.Threshold, errEmptySubmissionTag
	}

	st := types.SubmissionTag(submissionTags[0])

	switch st {
	case types.End, types.Submit, types.Threshold:
		return st, nil
	default:
		return st, errInvalidSubmissionTag
	}
}
