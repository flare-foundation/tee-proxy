package server

import (
	"encoding/json"
	"net/http"

	"github.com/flare-foundation/tee-node/pkg/types"

	"github.com/flare-foundation/tee-proxy/internal/service/action"
	"github.com/flare-foundation/tee-proxy/internal/service/result"

	"github.com/flare-foundation/tee-proxy/pkg/redis"
)

type Internal struct {
	actionService *action.Service
	resultService *result.Service
	server        http.Server
}

func (i *Internal) resultHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	result := new(types.ActionResponse)
	err := json.NewDecoder(r.Body).Decode(&result) // todo limit the size of the body
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest) // handle error
		return
	}

	err = i.resultService.StoreResponse(ctx, result)
	if err != nil {
		http.Error(w, "todo", http.StatusBadRequest) // handle error
		return
	}
}

func (i *Internal) dequeueHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	queueID := redis.QueueID(r.PathValue("queueID"))

	switch queueID {
	case redis.Main, redis.Read:
		value, err := i.actionService.Pop(ctx, queueID)
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
