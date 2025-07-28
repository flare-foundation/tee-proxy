package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/service/action"
	"github.com/flare-foundation/tee-proxy/internal/service/result"

	"github.com/flare-foundation/tee-proxy/pkg/queue"
	"github.com/flare-foundation/tee-proxy/pkg/status"
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

func (i *Internal) registerRoutes() {
	mux := http.NewServeMux()
	i.server.Handler = mux

	mux.HandleFunc("POST /result", prepareHandler(i.resH))
	mux.HandleFunc("POST /queue/{queueID}", prepareHandler(i.deqH))
}

func (i *Internal) resH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	// todo: Limit the size of the body
	response := new(types.ActionResponse)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&response)
	if err != nil {
		return ErrInvalidBody
	}

	err = i.resultService.Store(ctx, response)
	if err != nil {
		return err
	}
	return nil
}

func (i *Internal) deqH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	queueID := queue.QueueID(r.PathValue("queueID"))

	switch queueID {
	case queue.Main, queue.Read:
		value, err := i.actionService.DequeueAction(ctx, queueID)
		if err != nil {
			return err
		}

		// fmt.Println("action", value.Data)

		err = json.NewEncoder(w).Encode(value)
		if err != nil {
			return err
		}

	default:
		return fmt.Errorf("%w: invalid queueID", status.HTTP[400])
	}

	return nil
}
