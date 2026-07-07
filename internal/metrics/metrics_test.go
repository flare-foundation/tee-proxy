package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// gatheredNames returns the metric family names currently registered.
func gatheredNames(t *testing.T, m *Metrics) []string {
	t.Helper()

	families, err := m.Registry().Gather()
	require.NoError(t, err)

	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}

	return names
}

func TestNilMetricsIsNoOp(t *testing.T) {
	var m *Metrics

	require.False(t, m.Enabled())
	require.Nil(t, m.Registry())
}

func TestDisabledRegistersNothing(t *testing.T) {
	m := New(Config{Enable: false, Runtime: true})

	require.False(t, m.Enabled())
	require.Empty(t, gatheredNames(t, m), "disabled metrics must register no collectors")
}

func TestRuntimeGroupGatesBaseline(t *testing.T) {
	// Enabled but with the runtime group off: the endpoint is active yet no
	// baseline collectors are registered.
	off := New(Config{Enable: true, Runtime: false})
	require.True(t, off.Enabled())
	require.Empty(t, gatheredNames(t, off))

	// Runtime group on: go_*, process_*, and teeproxy_build_info are present.
	on := New(Config{Enable: true, Runtime: true})
	require.True(t, on.Enabled())

	names := gatheredNames(t, on)
	require.Contains(t, names, "teeproxy_build_info")

	var hasGo, hasProcess bool
	for _, n := range names {
		if strings.HasPrefix(n, "go_") {
			hasGo = true
		}
		if strings.HasPrefix(n, "process_") {
			hasProcess = true
		}
	}
	require.True(t, hasGo, "expected go_* runtime collectors")
	require.True(t, hasProcess, "expected process_* runtime collectors")
}

func TestHTTPMetrics(t *testing.T) {
	m := New(Config{Enable: true, HTTP: true})
	require.True(t, m.HTTPEnabled())

	m.ObserveHTTP("internal", "GET /healthy", 200, 5*time.Millisecond)
	m.ObserveHTTP("external", "", 503, 2*time.Millisecond) // empty route -> "unmatched"

	require.Equal(t, float64(1), testutil.ToFloat64(m.httpRequests.WithLabelValues("internal", "GET /healthy", "2xx")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.httpRequests.WithLabelValues("external", "unmatched", "5xx")))
	require.Equal(t, 2, testutil.CollectAndCount(m.httpDuration))
}

func TestHTTPDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, HTTP: false})
	require.False(t, m.HTTPEnabled())
	require.NotPanics(t, func() { m.ObserveHTTP("internal", "GET /x", 200, time.Millisecond) })
}

func TestStorageObserver(t *testing.T) {
	on := New(Config{Enable: true, Storage: true})
	require.NotNil(t, on.StorageObserver())

	on.Observe("redis", "results", "get", "success", time.Millisecond)
	require.Equal(t, float64(1), testutil.ToFloat64(on.storageOps.WithLabelValues("redis", "results", "get", "success")))
	require.Equal(t, 1, testutil.CollectAndCount(on.storageDuration), "the operation must also be recorded in the duration histogram")

	off := New(Config{Enable: true, Storage: false})
	require.Nil(t, off.StorageObserver(), "disabled storage group must yield a nil observer")
}

func TestResultMetrics(t *testing.T) {
	m := New(Config{Enable: true, Result: true})

	m.ResultProcessed(op.KeyGenerate.Hash(), 1) // known command, final
	m.ResultProcessed(common.Hash{}, 0)         // unknown command -> "other", failed

	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsProcessed.WithLabelValues("KEY_GENERATE", "final")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsProcessed.WithLabelValues("other", "failed")))

	m.ResultLost()
	m.ResultLost()
	require.Equal(t, float64(2), testutil.ToFloat64(m.resultsLost))

	m.ResultDiscarded()
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsDiscarded))

	m.ResultRejected("wrong_tee_id")
	m.ResultRejected("bad_signer")
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsRejected.WithLabelValues("wrong_tee_id")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsRejected.WithLabelValues("bad_signer")))
}

func TestResultDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Result: false})
	require.NotPanics(t, func() {
		m.ResultProcessed(op.KeyGenerate.Hash(), 1)
		m.ResultLost()
		m.ResultDiscarded()
		m.ResultRejected("wrong_tee_id")
	})
}

func TestResultStatusClass(t *testing.T) {
	require.Equal(t, "failed", resultStatusClass(0))
	require.Equal(t, "final", resultStatusClass(1))
	require.Equal(t, "transient", resultStatusClass(2))
	require.Equal(t, "transient", resultStatusClass(7))
}

func TestStatusClass(t *testing.T) {
	// Cover an interior value and both sides of every branch boundary: an off-by-one
	// edit (e.g. >= 500 -> > 500) would silently misclassify into the wrong class.
	tests := []struct {
		status int
		want   string
	}{
		{0, "1xx"}, // implicit/unset; the statusRecorder defaults to 200 to avoid this
		{100, "1xx"}, {199, "1xx"},
		{200, "2xx"}, {299, "2xx"},
		{300, "3xx"}, {399, "3xx"},
		{400, "4xx"}, {499, "4xx"},
		{500, "5xx"}, {599, "5xx"},
	}
	for _, tt := range tests {
		require.Equalf(t, tt.want, statusClass(tt.status), "status %d", tt.status)
	}
}

func TestOPCommandLabel(t *testing.T) {
	require.Equal(t, "KEY_GENERATE", opCommandLabel(op.KeyGenerate.Hash()))
	require.Equal(t, "TEE_INFO", opCommandLabel(op.TEEInfo.Hash()))
	require.Equal(t, "other", opCommandLabel(common.Hash{}))
}

func TestVotingMetrics(t *testing.T) {
	m := New(Config{Enable: true, Voting: true})

	m.InstructionReceived()
	m.InstructionReceived()
	m.InstructionRejected("wrong_tee_id")
	m.VotingStarted()
	m.VotingThresholdReached(250 * time.Millisecond)
	m.FinalizedActionEnqueueFailed()

	require.Equal(t, float64(2), testutil.ToFloat64(m.instructionsReceived))
	require.Equal(t, float64(1), testutil.ToFloat64(m.instructionsRejected.WithLabelValues("wrong_tee_id")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.votingsStarted))
	require.Equal(t, 1, testutil.CollectAndCount(m.votingThresholdDuration), "the finalization count is the histogram's _count")
	require.Equal(t, float64(1), testutil.ToFloat64(m.finalizedEnqueueFail))
}

func TestVotingDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Voting: false})
	require.NotPanics(t, func() {
		m.InstructionReceived()
		m.InstructionRejected("x")
		m.VotingStarted()
		m.VotingThresholdReached(time.Second)
		m.FinalizedActionEnqueueFailed()
	})
}

func TestQueueMetrics(t *testing.T) {
	m := New(Config{Enable: true, Queue: true})
	require.True(t, m.QueueEnabled())

	m.ActionDequeued("main", "success")
	m.ActionDequeued("main", "empty")
	m.ActionDequeued("main", "success")

	require.Equal(t, float64(2), testutil.ToFloat64(m.actionDequeued.WithLabelValues("main", "success")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.actionDequeued.WithLabelValues("main", "empty")))

	m.RegisterQueueDepth("main", func() float64 { return 5 })
	const expected = `
# HELP teeproxy_action_queue_depth Pending submission IDs per queue.
# TYPE teeproxy_action_queue_depth gauge
teeproxy_action_queue_depth{queue="main"} 5
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_action_queue_depth"))
}

func TestQueueDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Queue: false})
	require.False(t, m.QueueEnabled())

	called := false
	m.RegisterQueueDepth("main", func() float64 { called = true; return 1 })

	n, err := testutil.GatherAndCount(m.Registry(), "teeproxy_action_queue_depth")
	require.NoError(t, err)
	require.Zero(t, n)
	require.False(t, called)
	require.NotPanics(t, func() {
		m.ActionDequeued("main", "success")
	})
}

func TestInfoRefreshFailures(t *testing.T) {
	m := New(Config{Enable: true, Info: true})

	m.InfoRefreshFailed("wait_response")
	m.InfoRefreshFailed("wait_response")
	m.InfoRefreshFailed("verify_signature")

	require.Equal(t, float64(2), testutil.ToFloat64(m.infoRefreshFailures.WithLabelValues("wait_response")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.infoRefreshFailures.WithLabelValues("verify_signature")))
}

func TestInfoRefreshObserved(t *testing.T) {
	m := New(Config{Enable: true, Info: true})

	m.InfoRefreshObserved(120*time.Millisecond, nil)
	m.InfoRefreshObserved(30*time.Second, errors.New("boom"))

	// One histogram series per outcome label (ok, error).
	require.Equal(t, 2, testutil.CollectAndCount(m.infoRefreshDuration))
}

func TestAttestationMetrics(t *testing.T) {
	m := New(Config{Enable: true, Attestation: true})

	m.AttestationVerified("ok", "ok")
	m.AttestationVerified("error", "pubkey_mismatch")

	require.Equal(t, float64(1), testutil.ToFloat64(m.attestationVerify.WithLabelValues("ok", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.attestationVerify.WithLabelValues("error", "pubkey_mismatch")))
}

func TestInfoAttestationDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Info: false, Attestation: false})
	require.NotPanics(t, func() {
		m.InfoRefreshFailed("x")
		m.InfoRefreshObserved(time.Second, nil)
		m.AttestationVerified("ok", "ok")
	})
}

func TestNodeWaitMetrics(t *testing.T) {
	m := New(Config{Enable: true, Node: true})

	m.ObserveNodeWait("info", 250*time.Millisecond, nil)
	m.ObserveNodeWait("wallet_key_proof", 5*time.Second, context.DeadlineExceeded)
	m.ObserveNodeWait("machinepath", time.Second, errors.New("boom"))

	require.Equal(t, float64(1), testutil.ToFloat64(m.nodeWaitTotal.WithLabelValues("info", "ok")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.nodeWaitTotal.WithLabelValues("wallet_key_proof", "timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.nodeWaitTotal.WithLabelValues("machinepath", "error")))
	require.Equal(t, 3, testutil.CollectAndCount(m.nodeWaitDuration))
}

func TestNodeWaitResult(t *testing.T) {
	require.Equal(t, "ok", nodeWaitResult(nil))
	require.Equal(t, "timeout", nodeWaitResult(context.DeadlineExceeded))
	require.Equal(t, "timeout", nodeWaitResult(fmt.Errorf("waiting: %w", context.DeadlineExceeded)))
	require.Equal(t, "cancelled", nodeWaitResult(context.Canceled))
	require.Equal(t, "error", nodeWaitResult(errors.New("other")))
}

func TestNodeDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Node: false})
	require.Empty(t, gatheredNames(t, m), "disabled node group registers no collectors")
	require.NotPanics(t, func() { m.ObserveNodeWait("info", time.Second, nil) })
}

func TestPolicyMetrics(t *testing.T) {
	m := New(Config{Enable: true, Policy: true})

	m.SetActiveRewardEpoch(42)
	require.Equal(t, float64(42), testutil.ToFloat64(m.policyEpoch))
	m.SetActiveRewardEpoch(43)
	require.Equal(t, float64(43), testutil.ToFloat64(m.policyEpoch))
}

func TestPolicyDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Policy: false})
	require.NotPanics(t, func() {
		m.SetActiveRewardEpoch(1)
	})
}

func TestLivenessMetrics(t *testing.T) {
	m := New(Config{Enable: true, Liveness: true})
	require.True(t, m.LivenessEnabled())

	m.SetReady(true)
	require.Equal(t, float64(1), testutil.ToFloat64(m.ready))
	m.SetReady(false)
	require.Equal(t, float64(0), testutil.ToFloat64(m.ready))

	m.RegisterInfoDelay(func() float64 { return 12 })
	const expected = `
# HELP teeproxy_info_service_delay_seconds Seconds since the last successful TEE info refresh.
# TYPE teeproxy_info_service_delay_seconds gauge
teeproxy_info_service_delay_seconds 12
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_info_service_delay_seconds"))
}

func TestLivenessDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Liveness: false})
	require.False(t, m.LivenessEnabled())

	called := false
	m.RegisterInfoDelay(func() float64 { called = true; return 1 })

	n, err := testutil.GatherAndCount(m.Registry(), "teeproxy_info_service_delay_seconds")
	require.NoError(t, err)
	require.Zero(t, n)
	require.False(t, called)
	require.NotPanics(t, func() { m.SetReady(true) })
}

func TestActiveParticipantGauges(t *testing.T) {
	on := New(Config{Enable: true, ActiveVoters: true})
	on.RegisterActiveDataProviderVoters(func() float64 { return 2 })
	on.RegisterActiveInitiators(func() float64 { return 4 })

	names := gatheredNames(t, on)
	require.Contains(t, names, "teeproxy_active_data_provider_voters")
	require.Contains(t, names, "teeproxy_active_initiators")

	const expected = `
# HELP teeproxy_active_initiators Distinct initiators (proposers) that opened at least one voting in the current reward epoch.
# TYPE teeproxy_active_initiators gauge
teeproxy_active_initiators 4
`
	require.NoError(t, testutil.GatherAndCompare(on.Registry(), strings.NewReader(expected), "teeproxy_active_initiators"))

	off := New(Config{Enable: true, ActiveVoters: false})
	called := false
	mark := func() float64 { called = true; return 1 }
	off.RegisterActiveDataProviderVoters(mark)
	off.RegisterActiveInitiators(mark)

	require.Empty(t, gatheredNames(t, off), "disabled active-voters group registers no participant gauges")
	require.False(t, called, "count functions must not be invoked when the group is disabled")
}

func TestTopProviderUnfinalizedProposals(t *testing.T) {
	const name = "teeproxy_top_provider_unfinalized_proposals"

	on := New(Config{Enable: true, ActiveVoters: true})
	on.RegisterTopUnfinalizedProposals(func() []ProviderPending {
		return []ProviderPending{
			{Provider: "0x000000000000000000000000000000000000000b", Pending: 3},
			{Provider: "0x000000000000000000000000000000000000000a", Pending: 1},
		}
	})

	const expected = `
# HELP teeproxy_top_provider_unfinalized_proposals Unfinalized proposals held by each of the top providers (top 3) in the current reward epoch; providers with none are omitted.
# TYPE teeproxy_top_provider_unfinalized_proposals gauge
teeproxy_top_provider_unfinalized_proposals{provider="0x000000000000000000000000000000000000000a"} 1
teeproxy_top_provider_unfinalized_proposals{provider="0x000000000000000000000000000000000000000b"} 3
`
	require.NoError(t, testutil.GatherAndCompare(on.Registry(), strings.NewReader(expected), name))

	// No provider with pending proposals -> no series at all.
	empty := New(Config{Enable: true, ActiveVoters: true})
	empty.RegisterTopUnfinalizedProposals(func() []ProviderPending { return nil })
	n, err := testutil.GatherAndCount(empty.Registry(), name)
	require.NoError(t, err)
	require.Zero(t, n, "no series when no provider has unfinalized proposals")

	// Disabled group -> collector not registered, callback never invoked.
	off := New(Config{Enable: true, ActiveVoters: false})
	called := false
	off.RegisterTopUnfinalizedProposals(func() []ProviderPending { called = true; return nil })
	require.Empty(t, gatheredNames(t, off))
	require.False(t, called)
}

// TestTopProviderCollectorNoStaleSeries proves the collector reports only the current top
// set each scrape: an address that leaves the top must not linger as a stale series (the
// reason a custom collector is used instead of a GaugeVec).
func TestTopProviderCollectorNoStaleSeries(t *testing.T) {
	const (
		name  = "teeproxy_top_provider_unfinalized_proposals"
		addrA = "0x000000000000000000000000000000000000000a"
		addrB = "0x000000000000000000000000000000000000000b"
	)

	m := New(Config{Enable: true, ActiveVoters: true})
	var current []ProviderPending
	m.RegisterTopUnfinalizedProposals(func() []ProviderPending { return current })

	current = []ProviderPending{{Provider: addrA, Pending: 2}}
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(`
# HELP teeproxy_top_provider_unfinalized_proposals Unfinalized proposals held by each of the top providers (top 3) in the current reward epoch; providers with none are omitted.
# TYPE teeproxy_top_provider_unfinalized_proposals gauge
teeproxy_top_provider_unfinalized_proposals{provider="0x000000000000000000000000000000000000000a"} 2
`), name))

	// A now leaves the top and B enters: the scrape must show only B, never a stale A.
	current = []ProviderPending{{Provider: addrB, Pending: 5}}
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(`
# HELP teeproxy_top_provider_unfinalized_proposals Unfinalized proposals held by each of the top providers (top 3) in the current reward epoch; providers with none are omitted.
# TYPE teeproxy_top_provider_unfinalized_proposals gauge
teeproxy_top_provider_unfinalized_proposals{provider="0x000000000000000000000000000000000000000b"} 5
`), name))

	n, err := testutil.GatherAndCount(m.Registry(), name)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the current top set is reported; no stale per-address series accumulate")
}

func TestOldestStoredPolicyGauge(t *testing.T) {
	on := New(Config{Enable: true, Policy: true})
	require.True(t, on.PolicyEnabled())

	on.RegisterOldestStoredPolicy(func() float64 { return 100 })

	const expected = `
# HELP teeproxy_signing_policy_oldest_reward_epoch Oldest reward epoch with a signing policy still resident in the in-memory voting window.
# TYPE teeproxy_signing_policy_oldest_reward_epoch gauge
teeproxy_signing_policy_oldest_reward_epoch 100
`
	require.NoError(t, testutil.GatherAndCompare(on.Registry(), strings.NewReader(expected),
		"teeproxy_signing_policy_oldest_reward_epoch"))

	off := New(Config{Enable: true, Policy: false})
	require.False(t, off.PolicyEnabled())

	called := false
	off.RegisterOldestStoredPolicy(func() float64 { called = true; return 1 })

	n, err := testutil.GatherAndCount(off.Registry(), "teeproxy_signing_policy_oldest_reward_epoch")
	require.NoError(t, err)
	require.Zero(t, n)
	require.False(t, called, "epoch function must not be invoked when the policy group is disabled")
}
