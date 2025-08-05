package status

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestError(t *testing.T) {
	for j := range HTTP {
		require.Equal(t, j, ErrToCode(HTTP[j]))
	}

	rErr := errors.New("random")
	require.Equal(t, -1, ErrToCode(rErr))

	wError := fmt.Errorf("wrapped %w error", HTTP[400])
	require.Equal(t, 400, ErrToCode(wError))
}

func TestHandleError(t *testing.T) {
	rr := httptest.NewRecorder()

	HandleError(rr, errors.New("random error"))

	require.Equal(t, 500, rr.Result().StatusCode)
	require.Equal(t, "text/plain; charset=utf-8", rr.Result().Header.Get("Content-Type"))

	reason := rr.Body.String()
	require.Equal(t, "internal server error\n", reason)

	r400 := httptest.NewRecorder()
	err := fmt.Errorf("%w: some error", HTTP[400])

	for range 5 {
		err = fmt.Errorf("wrap %w", err)
	}

	HandleError(r400, err)

	require.Equal(t, 400, r400.Result().StatusCode)
	require.Equal(t, "text/plain; charset=utf-8", r400.Result().Header.Get("Content-Type"))

	reason = r400.Body.String()
	require.Equal(t, fmt.Sprintf("%s\n", err.Error()), reason)
}
