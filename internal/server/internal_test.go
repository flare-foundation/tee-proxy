package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/stretchr/testify/require"
)

// slowResultService blocks in ProcessAndStore, then signals completion.
type slowResultService struct {
	stored chan struct{}
	delay  time.Duration
}

func (s *slowResultService) ProcessAndStore(context.Context, *types.ActionResponse) error {
	time.Sleep(s.delay)
	close(s.stored)
	return nil
}

func (s *slowResultService) Serve(context.Context, common.Hash, types.SubmissionTag) (*types.ActionResponse, error) {
	return nil, nil
}

// TestInternalCloseWaitsForResultWrites guards the shutdown ordering: a result acked
// to the TEE node must be durably stored before Close returns (and the storage
// client is closed after it).
func TestInternalCloseWaitsForResultWrites(t *testing.T) {
	svc := &slowResultService{stored: make(chan struct{}), delay: 200 * time.Millisecond}
	i := &Internal{resultService: svc, server: &http.Server{}}

	body, err := json.Marshal(new(types.ActionResponse))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/result", bytes.NewReader(body))
	require.NoError(t, i.resultH(httptest.NewRecorder(), req))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, i.Close(ctx))

	select {
	case <-svc.stored:
	default:
		t.Fatal("Close returned before the detached result write completed")
	}
}
