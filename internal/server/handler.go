package server

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

var ErrInvalidBody = fmt.Errorf("%w: invalid body", status.HTTP[400])

func prepareHandler(f func(http.ResponseWriter, *http.Request) error, isInternal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

		err := f(w, r)
		if err != nil {
			if isInternal {
				handleErrorInternal(w, err)
			} else {
				handleErrorExternal(w, err)
			}
		}
	}
}

// handleErrorExternal replies to unsuccessful request.
// If error is wrapped HTTP error, status is retrieved, and error is given in response.
// Otherwise, status 500 and "internal server error" is given in the reply.
func handleErrorExternal(w http.ResponseWriter, err error) {
	code := status.ErrToCode(err)
	reason := err.Error()
	if code == -1 {
		code = http.StatusInternalServerError
		reason = "internal processing error"

		logger.Warnf("error processing external request: %s", err)
	}

	http.Error(w, reason, code)
}

// handleErrorInternal replies to unsuccessful request.
// If error is wrapped HTTP error, status is retrieved, and error is given in response.
// Otherwise, status 500 and "internal server error" is given in the reply.
func handleErrorInternal(w http.ResponseWriter, err error) {
	code := status.ErrToCode(err)
	reason := err.Error()
	if code == -1 {
		code = http.StatusInternalServerError
		reason = "internal processing error"
	}
	logger.Errorf("error processing internal request: %s", err)

	http.Error(w, reason, code)
}

// hashParam extract and validates 32 bytes hex string path parameter from request.
func hashParam(r *http.Request, param string) (common.Hash, error) {
	s := r.PathValue(param)

	s = strings.ToLower(s)
	s, _ = strings.CutPrefix(s, "0x")

	sB, err := hex.DecodeString(s)
	if err != nil {
		return common.Hash{}, invalidParam(param)
	}
	if len(sB) != 32 {
		return common.Hash{}, invalidParam(param)
	}

	return common.BytesToHash(sB), nil
}

// uint64Param extracts and validates uint64 path parameter from request.
func uint64Param(r *http.Request, param string) (uint64, error) {
	s := r.PathValue(param)
	s64, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, invalidParam(param)
	}

	return s64, err
}

// uint32Param extracts and validates uint32 path parameter from request.
func uint32Param(r *http.Request, param string) (uint32, error) {
	s := r.PathValue(param)
	s64, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, invalidParam(param)
	}

	return uint32(s64), err
}

func invalidParam(param string) error {
	return fmt.Errorf("%w: invalid %s", status.HTTP[400], param)
}
