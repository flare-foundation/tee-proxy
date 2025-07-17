package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/service/action"
	"github.com/flare-foundation/tee-proxy/internal/service/result"

	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/wallets"
)

type Internal struct {
	actionService *action.Service
	resultService *result.Service
	server        *http.Server
}

func NewInternal(port string,
	actionService *action.Service,
	resultService *result.Service,
	wallet *wallets.Storage) *Internal {
	addr := fmt.Sprintf(":%s", port)

	server := &http.Server{
		Addr: addr,
		// ReadTimeout:                  0,
		// ReadHeaderTimeout:            0,
		// WriteTimeout:                 0,
		// IdleTimeout:                  0,
		// MaxHeaderBytes:               0,
	}

	e := Internal{
		actionService: actionService,
		resultService: resultService,
		server:        server,
	}

	e.registerRoutes()

	return &e
}

func (i *Internal) Serve() error {
	return i.server.ListenAndServe()
}

func (i *Internal) resultHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	response := new(types.ActionResponse)
	err := json.NewDecoder(r.Body).Decode(&response) // todo limit the size of the body
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest) // handle error
		return
	}

	err = i.resultService.Store(ctx, response)
	if err != nil {
		http.Error(w, "todo", http.StatusBadRequest) // handle error
		return
	}
}

func (i *Internal) dequeueHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	queueID := queue.QueueID(r.PathValue("queueID"))

	switch queueID {
	case queue.Main, queue.Read:
		value, err := i.actionService.DequeueAction(ctx, queueID)
		if err != nil {
			http.Error(w, "todo", http.StatusInternalServerError) //handle empty queue
		}

		err = json.NewEncoder(w).Encode(value)
		if err != nil {
			http.Error(w, "todo", http.StatusInternalServerError)
		}

	default:
		http.Error(w, "invalid queue ID", http.StatusBadRequest)
		return
	}
}

func (i *Internal) registerRoutes() {
	mux := http.NewServeMux()
	i.server.Handler = mux

	mux.HandleFunc("POST /queue/{queueID}", i.dequeueHandler)
	mux.HandleFunc("POST /result", i.resultHandler)
}
