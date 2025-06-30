package server

import (
	"encoding/json"
	"net/http"
)

const maxBodySize = int64(1024 * 20) // 20 KB

func HandlerGenerator[T any, R any](f func(req *T) (*R, error)) http.HandlerFunc {
	// TODO: perhaps more strict measures for the size?

	return func(w http.ResponseWriter, r *http.Request) {
		var req T
		// Check if the request body size exceeds the limit
		if r.ContentLength > maxBodySize {
			http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
			return
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		res, err := f(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError) // TODO better error handling
			return
		}
		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(res)
		if err != nil {
			http.Error(w, "Failed to encode JSON", http.StatusInternalServerError)
			return
		}
	}
}
