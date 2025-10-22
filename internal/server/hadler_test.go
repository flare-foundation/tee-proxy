package server

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/flare-foundation/tee-proxy/pkg/status"
	"github.com/stretchr/testify/require"
)

func TestHandleError(t *testing.T) {
	rr := httptest.NewRecorder()

	handleErrorExternal(rr, errors.New("random error"))

	require.Equal(t, 500, rr.Result().StatusCode)
	require.Equal(t, "text/plain; charset=utf-8", rr.Result().Header.Get("Content-Type"))

	reason := rr.Body.String()
	require.Equal(t, "internal processing error\n", reason)

	r400 := httptest.NewRecorder()
	err := fmt.Errorf("%w: some error", status.HTTP[400])

	for range 5 {
		err = fmt.Errorf("wrap %w", err)
	}

	handleErrorInternal(r400, err)

	require.Equal(t, 400, r400.Result().StatusCode)
	require.Equal(t, "text/plain; charset=utf-8", r400.Result().Header.Get("Content-Type"))

	reason = r400.Body.String()
	require.Equal(t, fmt.Sprintf("%s\n", err.Error()), reason)
}
