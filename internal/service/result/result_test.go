package result

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLastStorageErrSetAndClear covers the auto-recovery contract: the most recent
// storage outcome wins, so a successful store clears the prior error and liveness
// recovers without a pod restart.
func TestLastStorageErrSetAndClear(t *testing.T) {
	s := NewService(nil)

	require.NoError(t, s.LastStorageErr())

	boom := errors.New("redis down")
	s.recordStorageResult(boom)
	require.ErrorIs(t, s.LastStorageErr(), boom)

	s.recordStorageResult(nil)
	require.NoError(t, s.LastStorageErr())
}
