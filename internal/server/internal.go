package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

type ResultService interface {
	ProcessAndStore(context.Context, *types.ActionResponse) error
	Serve(context.Context, common.Hash, types.SubmissionTag) (*types.ActionResponse, error)
}

type Internal struct {
	actionQueues  *queue.ActionQueues
	resultService ResultService
	server        *http.Server
}

func NewInternal(port string,
	actionQueues *queue.ActionQueues,
	resultService ResultService,
	wallet *wallets.Service) *Internal {
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
		actionQueues:  actionQueues,
		resultService: resultService,
		server:        server,
	}

	e.registerRoutes()

	return &e
}

// Serve starts the server.
func (i *Internal) Serve() error {
	logger.Infof("serving internal at %s", i.server.Addr)
	return i.server.ListenAndServe()
}

// Close gracefully closes the server.
func (i *Internal) Close(ctx context.Context) error {
	return i.server.Shutdown(ctx)
}

func (i *Internal) registerRoutes() {
	mux := http.NewServeMux()
	i.server.Handler = mux

	mux.HandleFunc("POST /result", prepareHandler(i.resultH, true))
	mux.HandleFunc("POST /queue/{queueID}", prepareHandler(i.queueH, true))
}

// resultH serves "/result" endpoint.
// It stores action response from the request body.
func (i *Internal) resultH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	// todo: Limit the size of the body
	response := new(types.ActionResponse)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&response)
	if err != nil {
		return ErrInvalidBody
	}

	err = i.resultService.ProcessAndStore(ctx, response)
	if err != nil {
		return err
	}
	return nil
}

// queueH serves "/queue/{queueID}" endpoint.
// It dequeue next action from the indicated queue.
// It returns nil body if the queue is empty
func (i *Internal) queueH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	queueID := processorutils.QueueID(r.PathValue("queueID"))

	switch queueID {
	case processorutils.Main, processorutils.Direct:
		value, err := i.actionQueues.Dequeue(ctx, queueID)
		if errors.Is(err, storage.ErrEmptyQueue) {
			return json.NewEncoder(w).Encode(nil)
		}
		if err != nil {
			return err
		}

		err = json.NewEncoder(w).Encode(value)
		if err != nil {
			return err
		}

	default:
		return fmt.Errorf("%w: invalid queueID", status.HTTP[400])
	}

	return nil
}
