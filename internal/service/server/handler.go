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

func prepareHandler(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)

		if err != nil {
			handleError(w, err)
		}
	}
}

// handleError replies to unsuccessful request.
// If error is wrapped HTTP error, status is retrieved, and error is given in response.
// Otherwise, status 500 and "internal server error" is given in the reply.
func handleError(w http.ResponseWriter, err error) {
	code := status.ErrToCode(err)
	reason := err.Error()
	if code == -1 {
		logger.Errorf("internal error: %s", err)
		code = http.StatusInternalServerError
		reason = "internal server error"
	}

	http.Error(w, reason, code)
}

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

func uint64Param(r *http.Request, param string) (uint64, error) {
	s := r.PathValue(param)
	s64, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, invalidParam(param)
	}

	return s64, err
}

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
