package server

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-proxy/pkg/status"
)

var ErrInvalidBody = fmt.Errorf("%w: invalid body", status.HTTP[400])

func prepareHandler(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)

		if err != nil {
			status.HandleError(w, err)
		}
	}
}

func hashParam(r *http.Request, param string) (common.Hash, error) {
	s := r.PathValue(param)

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
