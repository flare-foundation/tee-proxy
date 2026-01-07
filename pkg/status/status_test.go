package status

import (
	"errors"
	"fmt"
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

func TestAdd(t *testing.T) {
	err := errors.New("random")
	wErr := Add(err, 400)

	require.Equal(t, 400, ErrToCode(wErr))
}
