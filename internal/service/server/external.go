package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	cinstruction "github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/action"
	"github.com/flare-foundation/tee-proxy/internal/service/instruction"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/pkg/info"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
)

const (
	actionID      = "actionID"
	instructionID = "instructionID"
	keyID         = "keyID"
	rewardEpochID = "rewardEpochID"
	walletID      = "walletID"
)

type External struct {
	instructionService *instruction.Service
	actionService      *action.Service
	resultService      *result.Service
	server             *http.Server

	info   *info.Storage
	wallet *wallets.Storage
}

func NewExternal(port string,
	instructionService *instruction.Service,
	actionService *action.Service,
	resultService *result.Service,
	info *info.Storage,
	wallet *wallets.Storage) *External {
	addr := fmt.Sprintf(":%s", port)

	server := &http.Server{
		Addr: addr,
		// ReadTimeout:                  0,
		// ReadHeaderTimeout:            0,
		// WriteTimeout:                 0,
		// IdleTimeout:                  0,
		// MaxHeaderBytes:               0,
	}

	e := External{
		instructionService: instructionService,
		actionService:      actionService,
		resultService:      resultService,
		server:             server,
		info:               info,
		wallet:             wallet,
	}

	e.registerRoutes()

	return &e
}

func (e *External) Serve() error {
	return e.server.ListenAndServe()
}

func (e *External) instH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	i := new(cinstruction.Instruction)
	err := json.NewDecoder(r.Body).Decode(&i)
	if err != nil {
		return ErrInvalidBody
	}

	receipt, err := e.instructionService.ServeInstruction(ctx, i)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(receipt)
	if err != nil {
		return err
	}

	return nil
}

func (e *External) resH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	id, err := hashParam(r, actionID)
	if err != nil {
		return err
	}

	result, err := e.resultService.Serve(ctx, id)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		return err
	}

	return nil
}

func (e *External) rewH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	id, err := hashParam(r, actionID)
	if err != nil {
		return err
	}
	result, err := e.resultService.ServeRewards(ctx, id)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		return err
	}

	return nil
}

func (e *External) statH(w http.ResponseWriter, r *http.Request) error {
	id, err := hashParam(r, instructionID)
	if err != nil {
		return err
	}

	reID, err := uint32Param(r, rewardEpochID)
	if err != nil {
		return err
	}

	result, err := e.instructionService.Status(id, reID)
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		return err
	}

	return nil
}

// decide on the response type
func (e *External) infoH(w http.ResponseWriter, r *http.Request) error {
	e.info.RLock()
	defer e.info.RUnlock()

	result := e.info.Latest

	if result == nil {
		return fmt.Errorf("%w: proxy not initialized", status.HTTP[503])
	}

	err := json.NewEncoder(w).Encode(result)
	if err != nil {
		return err
	}

	return nil
}

// decide on the response type
func (e *External) walH(w http.ResponseWriter, r *http.Request) error {
	wID, err := hashParam(r, walletID)
	if err != nil {
		return err
	}

	kID, err := uint64Param(r, keyID)
	if err != nil {
		return err
	}

	walletInfo, err := e.wallet.KeyInfo(wID, kID) //todo format this
	if err != nil {
		return err
	}

	err = json.NewEncoder(w).Encode(walletInfo)
	if err != nil {
		return err
	}
	return nil
}

func (e *External) registerRoutes() {
	mux := http.NewServeMux()
	e.server.Handler = mux

	mux.HandleFunc("POST /instruction", prepareHandler(e.instH))
	mux.HandleFunc(fmt.Sprintf("GET /action/result/{%s}", actionID), prepareHandler(e.resH))
	mux.HandleFunc(fmt.Sprintf("GET /action/rewarding-data/{%s}", actionID), prepareHandler(e.rewH))
	mux.HandleFunc(fmt.Sprintf("GET /action/status/{%s}/{%s}", rewardEpochID, instructionID), prepareHandler(e.statH))
	mux.HandleFunc("GET /info", prepareHandler(e.infoH))
	mux.HandleFunc(fmt.Sprintf("GET /wallet/{%s}/{%s}", walletID, keyID), prepareHandler(e.walH))
}
