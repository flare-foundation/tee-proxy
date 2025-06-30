package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	cinstruction "github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/info"
	"github.com/flare-foundation/tee-proxy/pkg/service/action"
	"github.com/flare-foundation/tee-proxy/pkg/service/instruction"
	"github.com/flare-foundation/tee-proxy/pkg/service/result"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
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

func (e *External) instructionHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	i := new(cinstruction.Instruction)
	err := json.NewDecoder(r.Body).Decode(&i)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest) // handle error
		return
	}

	receipt, err := e.instructionService.ServeInstruction(ctx, i) //  todo return signed receipt
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) // handle error
		return
	}

	err = json.NewEncoder(w).Encode(receipt)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) //handle empty queue
	}
}

func (e *External) resultHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("actionID")
	idB, err := hex.DecodeString(idStr)
	if err != nil {
		http.Error(w, "malformed actionID", http.StatusBadRequest)
		return
	}

	if len(idB) != 32 {
		http.Error(w, "invalid actionID length", http.StatusBadRequest)
		return
	}
	id := common.BytesToHash(idB)

	result, err := e.resultService.Serve(ctx, id)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) // handle error
		return
	}

	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) //handle empty queue
	}
}

func (e *External) rewardingHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("actionID")
	idB, err := hex.DecodeString(idStr)
	if err != nil {
		http.Error(w, "malformed actionID", http.StatusBadRequest)
		return
	}
	if len(idB) != 32 {
		http.Error(w, "invalid actionID length", http.StatusBadRequest)
		return
	}

	id := common.BytesToHash(idB)

	result, err := e.resultService.ServeRewards(ctx, id)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) // handle error
		return
	}

	err = json.NewEncoder(w).Encode(result)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) //handle
	}
}

// decide on the response type
func (e *External) infoHandler(w http.ResponseWriter, r *http.Request) {
	e.info.RLock()
	defer e.info.RUnlock()

	result := e.info.Latest

	if result == nil {
		http.Error(w, "todo", http.StatusInternalServerError) //handle empty queue
		return
	}

	err := json.NewEncoder(w).Encode(result)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) //handle
	}
}

// decide on the response type
func (e *External) walletHandler(w http.ResponseWriter, r *http.Request) {
	wIDStr := r.PathValue("walletID")
	wIDB, err := hex.DecodeString(wIDStr)
	if err != nil {
		http.Error(w, "malformed walletID", http.StatusBadRequest)
		return
	}
	if len(wIDB) != 32 {
		http.Error(w, "invalid walletID length", http.StatusBadRequest)
		return
	}
	wID := common.BytesToHash(wIDB)

	kIDStr := r.PathValue("keyID")

	kID, err := strconv.ParseUint(kIDStr, 10, 64)
	if err != nil {
		http.Error(w, "malformed keyID", http.StatusBadRequest)
		return
	}

	walletInfo, err := e.wallet.KeyInfo(wID, kID) //todo format this
	if err != nil {
		http.Error(w, "TODO", http.StatusBadRequest)
		return
	}

	err = json.NewEncoder(w).Encode(walletInfo)
	if err != nil {
		http.Error(w, "todo", http.StatusInternalServerError) //handle
	}
}

func (e *External) registerRoutes() {
	mux := http.NewServeMux()
	e.server.Handler = mux

	mux.HandleFunc("POST /instruction", e.instructionHandler)
	mux.HandleFunc("GET /action/result/{actionID}", e.resultHandler)
	mux.HandleFunc("GET /action/rewarding-data/{actionID}", e.rewardingHandler)
	mux.HandleFunc("GET /info", e.infoHandler)
	mux.HandleFunc("GET /wallet/{walletID}/{keyID}", e.walletHandler)
}
