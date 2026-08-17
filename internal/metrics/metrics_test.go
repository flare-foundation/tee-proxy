package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/pkg/attestation"
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
	// process_* metrics appear only on Linux (procfs-backed collector).
	if runtime.GOOS == "linux" {
		require.True(t, hasProcess, "expected process_* runtime collectors")
	}
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

	m.ResultMissingActionID()
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsNoActionID))

	m.ResultRejected("wrong_tee_id")
	m.ResultRejected("bad_signer")
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsRejected.WithLabelValues("wrong_tee_id")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultsRejected.WithLabelValues("bad_signer")))
	// The pre-initialized "bootstrap" series survives untouched, proving pre-init does not perturb Inc counts.
	require.Equal(t, float64(0), testutil.ToFloat64(m.resultsRejected.WithLabelValues("bootstrap")))

	m.ResultChannelDropped("key_actions")
	m.ResultChannelDropped("key_actions")
	m.ResultChannelDropped("backups")
	require.Equal(t, float64(2), testutil.ToFloat64(m.resultChannelDropped.WithLabelValues("key_actions")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.resultChannelDropped.WithLabelValues("backups")))
	require.Equal(t, float64(0), testutil.ToFloat64(m.resultChannelDropped.WithLabelValues("backup_trigger")))
}

func TestResultDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Result: false})
	require.NotPanics(t, func() {
		m.ResultProcessed(op.KeyGenerate.Hash(), 1)
		m.ResultLost()
		m.ResultMissingActionID()
		m.ResultRejected("wrong_tee_id")
		m.ResultChannelDropped("key_actions")
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
		m.FinalizedActionLost("build_error")
	})

	n, err := testutil.GatherAndCount(m.Registry(), "teeproxy_finalized_action_lost_total")
	require.NoError(t, err)
	require.Zero(t, n, "disabled voting group registers no finalized_action_lost series")
}

func TestFinalizedActionLostCounter(t *testing.T) {
	m := New(Config{Enable: true, Voting: true})

	m.FinalizedActionLost("build_error")
	m.FinalizedActionLost("send_cancelled")
	m.FinalizedActionLost("send_cancelled")
	m.FinalizedActionLost("send_failed")

	require.Equal(t, float64(1), testutil.ToFloat64(m.finalizedActionLost.WithLabelValues("build_error")))
	require.Equal(t, float64(2), testutil.ToFloat64(m.finalizedActionLost.WithLabelValues("send_cancelled")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.finalizedActionLost.WithLabelValues("send_failed")))
	require.Contains(t, gatheredNames(t, m), "teeproxy_finalized_action_lost_total")
}

// TestFinalizedActionLostPreInitialized proves all reasons are born at 0 so the first,
// possibly one-shot drop satisfies the increase(...)>0 alerts on build_error and send_failed.
func TestFinalizedActionLostPreInitialized(t *testing.T) {
	m := New(Config{Enable: true, Voting: true})

	for _, reason := range []string{"build_error", "send_cancelled", "send_failed"} {
		require.Zerof(t, testutil.ToFloat64(m.finalizedActionLost.WithLabelValues(reason)), "reason %q not pre-initialized", reason)
	}
	require.Equal(t, 3, testutil.CollectAndCount(m.finalizedActionLost), "all reason series must exist before any drop")
}

func TestFinalizedActionLostNilSafe(t *testing.T) {
	var m *Metrics
	require.NotPanics(t, func() { m.FinalizedActionLost("send_cancelled") })
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

	m.ActionEnqueued("main", "success")
	m.ActionEnqueued("main", "success")
	m.ActionEnqueued("main", "store_error")
	require.Equal(t, float64(2), testutil.ToFloat64(m.actionEnqueued.WithLabelValues("main", "success")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.actionEnqueued.WithLabelValues("main", "store_error")))

	m.QueueDepthReadFailed("main")
	const expectedReadFailures = `
# HELP teeproxy_action_queue_depth_read_failures_total Scrape-time queue-depth (LLEN) read failures by queue; while nonzero the depth gauge reports 0 and the backpressure alert is masked.
# TYPE teeproxy_action_queue_depth_read_failures_total counter
teeproxy_action_queue_depth_read_failures_total{queue="main"} 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expectedReadFailures), "teeproxy_action_queue_depth_read_failures_total"))
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
		m.ActionEnqueued("main", "success")
		m.QueueDepthReadFailed("main")
	})

	for _, name := range []string{"teeproxy_action_enqueue_total", "teeproxy_action_queue_depth_read_failures_total"} {
		count, err := testutil.GatherAndCount(m.Registry(), name)
		require.NoError(t, err)
		require.Zerof(t, count, "%s must register no series when the queue group is disabled", name)
	}
}

func TestInfoRefreshFailures(t *testing.T) {
	m := New(Config{Enable: true, Info: true})

	m.InfoRefreshFailed("wait_response")
	m.InfoRefreshFailed("wait_response")
	m.InfoRefreshFailed("verify_signature")

	require.Equal(t, float64(2), testutil.ToFloat64(m.infoRefreshFailures.WithLabelValues("wait_response")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.infoRefreshFailures.WithLabelValues("verify_signature")))
}

// TestInfoRefreshFailuresPreInitialized proves every refresh stage is born at 0, so the
// critical increase(...)>0 alert on a stage catches its first, possibly one-shot failure.
func TestInfoRefreshFailuresPreInitialized(t *testing.T) {
	m := New(Config{Enable: true, Info: true})

	stages := []string{
		"fetch_block", "create_action", "enqueue", "wait_response", "action_status",
		"unmarshal", "parse_tee_id", "signing_hash", "verify_signature", "verify_attestation",
		"unknown",
	}
	for _, stage := range stages {
		require.Zerof(t, testutil.ToFloat64(m.infoRefreshFailures.WithLabelValues(stage)), "stage %q not pre-initialized", stage)
		require.Zerof(t, testutil.ToFloat64(m.infoRefreshExhausted.WithLabelValues(stage)), "exhausted stage %q not pre-initialized", stage)
	}
	require.Equal(t, len(stages), testutil.CollectAndCount(m.infoRefreshFailures), "every stage series must exist before any failure")
	require.Equal(t, len(stages), testutil.CollectAndCount(m.infoRefreshExhausted), "every stage series must exist before any give-up")
}

func TestInfoRefreshExhausted(t *testing.T) {
	m := New(Config{Enable: true, Info: true})

	m.InfoRefreshExhausted("action_status")
	m.InfoRefreshExhausted("action_status")

	require.Equal(t, float64(2), testutil.ToFloat64(m.infoRefreshExhausted.WithLabelValues("action_status")))
	require.Equal(t, float64(0), testutil.ToFloat64(m.infoRefreshExhausted.WithLabelValues("wait_response")))
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
	// A pre-initialized error reason left un-incremented stays at 0: recording one reason does not disturb the others.
	require.Equal(t, float64(0), testutil.ToFloat64(m.attestationVerify.WithLabelValues("error", "token_too_old")))

	// An accepted magic_pass sentinel is a bounded ok/magic_pass value on the same counter.
	m.AttestationVerified("ok", "magic_pass")
	require.Equal(t, float64(1), testutil.ToFloat64(m.attestationVerify.WithLabelValues("ok", "magic_pass")))
}

func TestAttestationPosture(t *testing.T) {
	m := New(Config{Enable: true, Attestation: true})

	m.SetAttestationPosture(map[string]bool{"enabled": true, "magic_pass_allowed": true, "sec_boot_check": false})
	require.Equal(t, float64(1), testutil.ToFloat64(m.attestationPosture.WithLabelValues("enabled")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.attestationPosture.WithLabelValues("magic_pass_allowed")))
	require.Equal(t, float64(0), testutil.ToFloat64(m.attestationPosture.WithLabelValues("sec_boot_check")))

	// Set is idempotent: flipping a setting off updates the same series to 0.
	m.SetAttestationPosture(map[string]bool{"magic_pass_allowed": false})
	require.Equal(t, float64(0), testutil.ToFloat64(m.attestationPosture.WithLabelValues("magic_pass_allowed")))
}

func TestAttestationPostureDisabledIsNoOp(t *testing.T) {
	var nilMetrics *Metrics
	require.NotPanics(t, func() { nilMetrics.SetAttestationPosture(map[string]bool{"enabled": true}) })

	off := New(Config{Enable: true, Attestation: false})
	require.NotPanics(t, func() { off.SetAttestationPosture(map[string]bool{"enabled": true}) })
	require.Nil(t, off.attestationPosture, "disabled attestation group leaves the posture gauge nil")

	n, err := testutil.GatherAndCount(off.Registry(), "teeproxy_attestation_posture")
	require.NoError(t, err)
	require.Zero(t, n, "no attestation_posture series when the group is disabled")
}

// TestSecurityCountersPreInitialized proves the security-critical CounterVecs export every
// bounded series at 0 from startup, so the first, possibly one-shot event satisfies
// increase(...)>0 and the absent() meta-alerts see the families in a healthy idle proxy.
func TestSecurityCountersPreInitialized(t *testing.T) {
	m := New(Config{Enable: true, Result: true, Attestation: true})

	for _, reason := range []string{"bad_signer", "wrong_tee_id", "bootstrap"} {
		require.Zerof(t, testutil.ToFloat64(m.resultsRejected.WithLabelValues(reason)), "reason %q not pre-initialized", reason)
	}
	require.Equal(t, 3, testutil.CollectAndCount(m.resultsRejected))

	require.Zero(t, testutil.ToFloat64(m.attestationVerify.WithLabelValues("ok", "ok")))
	require.Zero(t, testutil.ToFloat64(m.attestationVerify.WithLabelValues("ok", "magic_pass")))
	require.Zero(t, testutil.ToFloat64(m.attestationVerify.WithLabelValues("error", "other")))
	// The two pre-initialized ok series (ok, magic_pass) plus one per error reason.
	require.Equal(t, 2+len(attestation.ErrorReasons), testutil.CollectAndCount(m.attestationVerify))

	names := gatheredNames(t, m)
	require.Contains(t, names, "teeproxy_results_rejected_total")
	require.Contains(t, names, "teeproxy_attestation_verify_total")
}

func TestInfoAttestationDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Info: false, Attestation: false})
	require.NotPanics(t, func() {
		m.InfoRefreshFailed("x")
		m.InfoRefreshExhausted("x")
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

	// 5 paths x {timeout,error} pre-initialized (so increase()>0 alerts fire on a first,
	// one-shot wait failure) + the one observed ok series above.
	require.Equal(t, 11, testutil.CollectAndCount(m.nodeWaitTotal), "timeout/error series must exist at 0 for every path")
	require.Equal(t, float64(0), testutil.ToFloat64(m.nodeWaitTotal.WithLabelValues("policy_update", "timeout")))
}

// TestNodeWaitPolicyUpdatePath pins policy_update as a valid bounded path label value on
// the node-wait collectors, matching the confirmation wait instrumented in the policy loop.
func TestNodeWaitPolicyUpdatePath(t *testing.T) {
	m := New(Config{Enable: true, Node: true})

	m.ObserveNodeWait("policy_update", 3*time.Second, nil)

	require.Equal(t, float64(1), testutil.ToFloat64(m.nodeWaitTotal.WithLabelValues("policy_update", "ok")))
	require.Equal(t, 1, testutil.CollectAndCount(m.nodeWaitDuration), "the confirmation wait must be recorded in the duration histogram")
}

func TestNodeWaitResult(t *testing.T) {
	require.Equal(t, "ok", nodeWaitResult(nil))
	require.Equal(t, "timeout", nodeWaitResult(context.DeadlineExceeded))
	require.Equal(t, "timeout", nodeWaitResult(fmt.Errorf("waiting: %w", context.DeadlineExceeded)))
	require.Equal(t, "timeout", nodeWaitResult(fmt.Errorf("waiting: %w", os.ErrDeadlineExceeded)))
	require.Equal(t, "timeout", nodeWaitResult(fmt.Errorf("waiting: %w", &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded})))
	require.Equal(t, "cancelled", nodeWaitResult(context.Canceled))
	require.Equal(t, "error", nodeWaitResult(errors.New("other")))
}

func TestNodeDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Node: false})
	require.Empty(t, gatheredNames(t, m), "disabled node group registers no collectors")
	require.NotPanics(t, func() {
		m.ObserveNodeWait("info", time.Second, nil)
		m.MachinepathPollObserved("confirmed")
	})
}

func TestMachinepathPollMetrics(t *testing.T) {
	m := New(Config{Enable: true, Node: true})

	results := []string{"fetch_error", "build_error", "no_authorization", "enqueue_error", "wait_error", "no_change", "confirmed", "rejected"}

	// All results pre-initialized at 0 so the poll-error warnings fire on the first occurrence.
	require.Equal(t, len(results), testutil.CollectAndCount(m.machinepathPollTotal), "all results must be pre-initialized")
	require.Equal(t, float64(0), testutil.ToFloat64(m.machinepathPollTotal.WithLabelValues("wait_error")))

	for _, result := range results {
		m.MachinepathPollObserved(result)
	}

	for _, result := range results {
		require.Equalf(t, float64(1), testutil.ToFloat64(m.machinepathPollTotal.WithLabelValues(result)), "result %q", result)
	}
	require.Equal(t, len(results), testutil.CollectAndCount(m.machinepathPollTotal), "each result must be its own series")
}

func TestGovernancePosture(t *testing.T) {
	m := New(Config{Enable: true, Node: true})

	m.SetGovernancePosture(map[string]bool{"configured": true, "safe_backed": false})
	require.Equal(t, float64(1), testutil.ToFloat64(m.governancePosture.WithLabelValues("configured")))
	require.Equal(t, float64(0), testutil.ToFloat64(m.governancePosture.WithLabelValues("safe_backed")))

	// Set is idempotent: flipping a setting updates the same series.
	m.SetGovernancePosture(map[string]bool{"safe_backed": true})
	require.Equal(t, float64(1), testutil.ToFloat64(m.governancePosture.WithLabelValues("safe_backed")))
}

func TestGovernancePostureDisabledIsNoOp(t *testing.T) {
	var nilMetrics *Metrics
	require.NotPanics(t, func() { nilMetrics.SetGovernancePosture(map[string]bool{"configured": true}) })

	off := New(Config{Enable: true, Node: false})
	require.NotPanics(t, func() { off.SetGovernancePosture(map[string]bool{"configured": true}) })
	require.Nil(t, off.governancePosture, "disabled node group leaves the posture gauge nil")

	n, err := testutil.GatherAndCount(off.Registry(), "teeproxy_governance_posture")
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestGovernanceHashMatchGauge(t *testing.T) {
	m := New(Config{Enable: true, Node: true})
	m.RegisterGovernanceHashMatch(func() float64 { return 1 })

	const expected = `
# HELP teeproxy_governance_hash_match 1 if the node's last-reported governance hash equals the proxy's startup snapshot, else 0.
# TYPE teeproxy_governance_hash_match gauge
teeproxy_governance_hash_match 1
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_governance_hash_match"))
}

func TestGovernanceHashMatchGaugeDisabled(t *testing.T) {
	m := New(Config{Enable: true, Node: false})

	matchCalled := false
	m.RegisterGovernanceHashMatch(func() float64 { matchCalled = true; return 1 })

	n, err := testutil.GatherAndCount(m.Registry(), "teeproxy_governance_hash_match")
	require.NoError(t, err)
	require.Zero(t, n, "must register no series when the node group is disabled")
	require.False(t, matchCalled)
}

func TestPolicyMetrics(t *testing.T) {
	m := New(Config{Enable: true, Policy: true})

	m.SetActiveRewardEpoch(42)
	require.Equal(t, float64(42), testutil.ToFloat64(m.policyEpoch))
	m.SetActiveRewardEpoch(43)
	require.Equal(t, float64(43), testutil.ToFloat64(m.policyEpoch))

	m.SetPolicyFetched()
	last := testutil.ToFloat64(m.policyLastFetch)
	require.Positive(t, last, "last-fetch timestamp must be set to the current time")
	require.InDelta(t, float64(time.Now().Unix()), last, 5, "last-fetch timestamp must be recent")

	m.PolicyUpdate("applied")
	m.PolicyUpdate("applied")
	m.PolicyUpdate("rejected")
	require.Equal(t, float64(2), testutil.ToFloat64(m.policyUpdate.WithLabelValues("applied")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.policyUpdate.WithLabelValues("rejected")))
}

func TestPolicyDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Policy: false})
	require.NotPanics(t, func() {
		m.SetActiveRewardEpoch(1)
		m.SetPolicyFetched()
		m.PolicyUpdate("applied")
	})

	for _, name := range []string{"teeproxy_policy_last_fetch_timestamp_seconds", "teeproxy_policy_update_total"} {
		n, err := testutil.GatherAndCount(m.Registry(), name)
		require.NoError(t, err)
		require.Zerof(t, n, "%s must register no series when the policy group is disabled", name)
	}
}

func TestPolicyRewardEpochGauges(t *testing.T) {
	m := New(Config{Enable: true, Policy: true})
	m.RegisterNodeAppliedPolicy(func() float64 { return 42 })
	m.RegisterMaxConsensusEpoch(func() float64 { return 41 })

	names := gatheredNames(t, m)
	require.Contains(t, names, "teeproxy_node_applied_policy_epoch")
	require.Contains(t, names, "teeproxy_consensus_max_reward_epoch")

	const expected = `
# HELP teeproxy_node_applied_policy_epoch Reward epoch of the signing policy the tee-node reports it has most recently applied.
# TYPE teeproxy_node_applied_policy_epoch gauge
teeproxy_node_applied_policy_epoch 42
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected), "teeproxy_node_applied_policy_epoch"))
}

func TestPolicyRewardEpochGaugesDisabled(t *testing.T) {
	m := New(Config{Enable: true, Policy: false})

	appliedCalled, consensusCalled := false, false
	m.RegisterNodeAppliedPolicy(func() float64 { appliedCalled = true; return 1 })
	m.RegisterMaxConsensusEpoch(func() float64 { consensusCalled = true; return 1 })

	for _, name := range []string{"teeproxy_node_applied_policy_epoch", "teeproxy_consensus_max_reward_epoch"} {
		n, err := testutil.GatherAndCount(m.Registry(), name)
		require.NoError(t, err)
		require.Zerof(t, n, "%s must register no series when the policy group is disabled", name)
	}
	require.False(t, appliedCalled)
	require.False(t, consensusCalled)
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

	m.RegisterCChainDelay(func() float64 { return 30 })
	const expectedCChain = `
# HELP teeproxy_cchain_indexer_delay_seconds Seconds since the last c-chain indexer block, read from the indexer DB at scrape time.
# TYPE teeproxy_cchain_indexer_delay_seconds gauge
teeproxy_cchain_indexer_delay_seconds 30
`
	require.NoError(t, testutil.GatherAndCompare(m.Registry(), strings.NewReader(expectedCChain), "teeproxy_cchain_indexer_delay_seconds"))
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

	cchainCalled := false
	m.RegisterCChainDelay(func() float64 { cchainCalled = true; return 1 })
	n, err = testutil.GatherAndCount(m.Registry(), "teeproxy_cchain_indexer_delay_seconds")
	require.NoError(t, err)
	require.Zero(t, n)
	require.False(t, cchainCalled)
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

func TestActiveVoterWeightGauges(t *testing.T) {
	tests := []struct {
		name     string
		register func(*Metrics, func() float64)
		value    float64
		expected string
	}{
		{
			name:     "teeproxy_active_data_provider_weight_bips",
			register: (*Metrics).RegisterActiveDataProviderWeight,
			value:    5001,
			expected: `
# HELP teeproxy_active_data_provider_weight_bips Combined signing-policy weight, in BIPS of the policy total, of the distinct data-provider voters counted by active_data_provider_voters.
# TYPE teeproxy_active_data_provider_weight_bips gauge
teeproxy_active_data_provider_weight_bips 5001
`,
		},
		{
			name:     "teeproxy_max_voting_weight_bips",
			register: (*Metrics).RegisterMaxVotingWeight,
			value:    3333,
			expected: `
# HELP teeproxy_max_voting_weight_bips Highest provider weight, in BIPS of the policy total, accumulated by any single voting in the reported reward epoch.
# TYPE teeproxy_max_voting_weight_bips gauge
teeproxy_max_voting_weight_bips 3333
`,
		},
		{
			name:     "teeproxy_voting_threshold_bips",
			register: (*Metrics).RegisterVotingThreshold,
			value:    5000,
			expected: `
# HELP teeproxy_voting_threshold_bips Signing-policy finalization threshold in BIPS of total voter weight for the reported reward epoch; default votings finalize on weight strictly greater than this.
# TYPE teeproxy_voting_threshold_bips gauge
teeproxy_voting_threshold_bips 5000
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			on := New(Config{Enable: true, ActiveVoters: true})
			test.register(on, func() float64 { return test.value })
			require.Contains(t, gatheredNames(t, on), test.name)
			require.NoError(t, testutil.GatherAndCompare(on.Registry(), strings.NewReader(test.expected), test.name))

			off := New(Config{Enable: true, ActiveVoters: false})
			called := false
			test.register(off, func() float64 { called = true; return 1 })
			require.Empty(t, gatheredNames(t, off), "disabled active-voters group registers no weight gauge")
			require.False(t, called, "the gauge function must not be invoked when the group is disabled")
		})
	}
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

func TestWalletSyncMetrics(t *testing.T) {
	m := New(Config{Enable: true, Wallet: true})
	require.True(t, m.WalletEnabled())

	// All results pre-initialized at 0 — the [3h]>=3 sustained alert undercounts a series born at 1.
	require.Equal(t, 5, testutil.CollectAndCount(m.walletSyncTotal), "all results must be pre-initialized")
	require.Equal(t, float64(0), testutil.ToFloat64(m.walletSyncTotal.WithLabelValues("wait_error")))

	for _, result := range []string{"success", "enqueue_error", "wait_error", "parse_error", "skipped"} {
		m.WalletSyncObserved(result)
	}

	for _, result := range []string{"success", "enqueue_error", "wait_error", "parse_error", "skipped"} {
		require.Equalf(t, float64(1), testutil.ToFloat64(m.walletSyncTotal.WithLabelValues(result)), "result %q", result)
	}
	require.Equal(t, 5, testutil.CollectAndCount(m.walletSyncTotal), "each result must be its own series")
}

func TestWalletKeyUpdateFailedCounter(t *testing.T) {
	m := New(Config{Enable: true, Wallet: true})

	m.WalletKeyUpdateFailed()
	m.WalletKeyUpdateFailed()

	require.Equal(t, float64(2), testutil.ToFloat64(m.walletKeyUpdateFailed))
	require.Contains(t, gatheredNames(t, m), "teeproxy_wallet_key_update_failed_total")
}

func TestWalletBackupApplyFailedCounter(t *testing.T) {
	m := New(Config{Enable: true, Wallet: true})

	m.WalletBackupApplyFailed()

	require.Equal(t, float64(1), testutil.ToFloat64(m.walletBackupApplyFailed))
	require.Contains(t, gatheredNames(t, m), "teeproxy_wallet_backup_apply_failed_total")
}

func TestWalletKeysCachedGauge(t *testing.T) {
	on := New(Config{Enable: true, Wallet: true})
	require.True(t, on.WalletEnabled())

	on.RegisterWalletKeysCached(func() float64 { return 3 })

	const expected = `
# HELP teeproxy_wallet_keys_cached Key proofs cached in the wallet service's in-memory store.
# TYPE teeproxy_wallet_keys_cached gauge
teeproxy_wallet_keys_cached 3
`
	require.NoError(t, testutil.GatherAndCompare(on.Registry(), strings.NewReader(expected), "teeproxy_wallet_keys_cached"))

	off := New(Config{Enable: true, Wallet: false})
	require.False(t, off.WalletEnabled())

	called := false
	off.RegisterWalletKeysCached(func() float64 { called = true; return 1 })

	n, err := testutil.GatherAndCount(off.Registry(), "teeproxy_wallet_keys_cached")
	require.NoError(t, err)
	require.Zero(t, n)
	require.False(t, called, "count function must not be invoked when the wallet group is disabled")
}

func TestWalletDisabledIsNoOp(t *testing.T) {
	m := New(Config{Enable: true, Wallet: false})
	require.False(t, m.WalletEnabled())
	require.NotPanics(t, func() {
		m.WalletSyncObserved("success")
		m.WalletKeyUpdateFailed()
		m.WalletBackupApplyFailed()
		m.RegisterWalletKeysCached(func() float64 { return 1 })
	})
}
