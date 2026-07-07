package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/internal/queue"
	"github.com/flare-foundation/tee-proxy/internal/service/result"
	"github.com/flare-foundation/tee-proxy/internal/service/wallets"
	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

const (
	maxResultBodySize = 25 * mib
)

var errInvalidQueueID = fmt.Errorf("%w: invalid queueID", status.HTTP[400])

const resultWriteTimeout = 10 * time.Second

// ResultService defines the interface for processing and serving action results.
type ResultService interface {
	ProcessAndStore(context.Context, *types.ActionResponse) error
	Serve(context.Context, common.Hash, types.SubmissionTag) (*types.ActionResponse, error)
}

var _ ResultService = &result.Service{}

// Internal is the system-facing HTTP server exposing action queue, result, and liveness endpoints.
type Internal struct {
	actionQueues *queue.ActionQueues

	resultService ResultService
	server        *http.Server

	lHandlers livenessHandlers

	metrics *metrics.Metrics
}

type liveness interface {
	Startup(context.Context) error
	Ready(context.Context) error
}

// NewInternal creates and configures a new Internal server listening on port.
// m may be nil or disabled, in which case no /metrics endpoint is mounted.
func NewInternal(port string,
	actionQueues *queue.ActionQueues,
	resultService ResultService,
	wallet *wallets.Service,
	liveness liveness,
	m *metrics.Metrics,
) *Internal {
	addr := fmt.Sprintf(":%s", port)

	server := &http.Server{
		Addr:              addr,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024, // 16 KB
	}

	e := Internal{
		actionQueues:  actionQueues,
		resultService: resultService,
		server:        server,
		lHandlers:     livenessHandlers{liveness},
		metrics:       m,
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

	mux.HandleFunc("POST /result", prepareHandler(i.resultH, maxResultBodySize, true))
	mux.HandleFunc("POST /queue/{queueID}", prepareHandler(i.queueH, noBody, true))

	mux.HandleFunc("GET /healthy", i.lHandlers.healthy)
	mux.HandleFunc("GET /startup", i.lHandlers.startup)
	mux.HandleFunc("GET /ready", i.lHandlers.ready)

	if i.metrics.Enabled() {
		mux.Handle("GET /metrics", promhttp.HandlerFor(i.metrics.Registry(), promhttp.HandlerOpts{
			// Bound concurrent scrapes: each fans out into LLEN-backed gauges and
			// lock-walks, so a misbehaving scraper must not pile them up.
			MaxRequestsInFlight: 3,
			EnableOpenMetrics:   true,
			// Surface gather/encode errors that are otherwise silently discarded.
			ErrorLog: promErrorLogger{},
		}))
	}

	i.server.Handler = instrumentHTTP(i.metrics, "internal", mux)
}

// promErrorLogger adapts the project logger to promhttp.Logger so /metrics gather and
// encoding errors are surfaced instead of being silently discarded.
type promErrorLogger struct{}

func (promErrorLogger) Println(v ...any) {
	logger.Errorf("serving /metrics: %s", fmt.Sprint(v...))
}

// resultH serves "/result" endpoint.
// It stores action response from the request body.
func (i *Internal) resultH(w http.ResponseWriter, r *http.Request) error {
	response := new(types.ActionResponse)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&response)
	if err != nil {
		return ErrInvalidBody
	}

	logger.Debugf("received response for %v tag: %v, status %v", response.Result.ID, response.Result.SubmissionTag, response.Result.Status)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resultWriteTimeout)
		defer cancel()

		if err := i.resultService.ProcessAndStore(ctx, response); err != nil {
			// Body has already been acked to the TEE node; surface any processing failure.
			// The lost-result metric is recorded inside ProcessAndStore, gated on the
			// storage outcome, so deliberate validation rejections are not counted as lost.
			logger.Errorf("processing result failed id=%s tag=%s opType=%s opCommand=%s: %v",
				response.Result.ID,
				response.Result.SubmissionTag,
				op.HashToOPType(response.Result.OPType),
				op.HashToOPCommand(response.Result.OPCommand),
				err)
		}
	}()

	return nil
}

// queueH serves "/queue/{queueID}" endpoint.
// It dequeue next action from the indicated queue.
// It returns nil body if the queue is empty
func (i *Internal) queueH(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()

	queueID := processorutils.QueueID(r.PathValue("queueID"))

	switch queueID {
	case processorutils.Main, processorutils.Direct, processorutils.Backup:
		value, err := i.actionQueues.Dequeue(ctx, queueID)
		if errors.Is(err, storage.ErrEmptyQueue) {
			return json.NewEncoder(w).Encode(nil)
		}
		if err != nil {
			return err
		}

		logger.Debugf("sending action %v with tag %v to the node on queue %v", value.Data.ID, value.Data.SubmissionTag, queueID)
		err = json.NewEncoder(w).Encode(value)
		if err != nil {
			return err
		}

	default:
		return errInvalidQueueID
	}

	return nil
}

type livenessHandlers struct {
	liveness
}

func (*livenessHandlers) healthy(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Length", "0")

	w.WriteHeader(http.StatusOK)
}

func (l *livenessHandlers) startup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")

	err := l.Startup(r.Context())

	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (i *livenessHandlers) ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")

	err := i.Ready(r.Context())

	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
}
