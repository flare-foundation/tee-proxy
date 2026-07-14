// Package metrics defines the Prometheus collectors the proxy exposes and the
// configuration that enables them. Metrics are off unless Config.Enable is set.
//
// A nil *Metrics is a valid no-op, so call sites wire unconditionally and a
// disabled group costs only a nil check.
package metrics

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime/debug"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/flare-foundation/tee-proxy/internal/version"
	"github.com/flare-foundation/tee-proxy/pkg/attestation"
	"github.com/flare-foundation/tee-proxy/pkg/storage"
)

// namespace prefixes every metric name, yielding teeproxy_*.
const namespace = "teeproxy"

// httpBuckets spans fast JSON replies up to the slowest handlers. The 30s/60s tail sits
// above the servers' 15s WriteTimeout on purpose: that timeout aborts only the response
// write, not the handler goroutine, so a degraded (e.g. Redis-stalled) request can still
// be observed taking longer than 15s.
var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// storageBuckets covers sub-millisecond Redis ops through slow backend calls.
var storageBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

// votingDurationBuckets covers sub-second consensus through votings that approach the
// proposal-expiration window (minutes).
var votingDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// nodeBuckets covers fast cached node replies through waits approaching the per-path
// response timeouts (wallet KEY_INFO/KEY_PROOF allow 3m, machinepath and policy_update
// confirmation 2m).
var nodeBuckets = []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 180, 300}

// infoRefreshBuckets covers a fast cached info refresh through one approaching the
// ~30s response-wait timeout.
var infoRefreshBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// Config selects which metric groups are collected.
// The zero value collects nothing; groups take effect only when Enable is true.
type Config struct {
	Enable       bool
	HTTP         bool
	Storage      bool
	Queue        bool
	Voting       bool
	ActiveVoters bool
	Result       bool
	Wallet       bool
	Info         bool
	Attestation  bool
	Policy       bool
	Liveness     bool
	Node         bool
	Runtime      bool
}

// Metrics owns the Prometheus registry and the collectors for the enabled groups.
// Collectors for disabled groups are left nil; all methods are safe on a nil receiver.
type Metrics struct {
	cfg Config
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec

	storageOps      *prometheus.CounterVec
	storageDuration *prometheus.HistogramVec

	resultsProcessed     *prometheus.CounterVec
	resultsLost          prometheus.Counter
	resultsDiscarded     prometheus.Counter
	resultsRejected      *prometheus.CounterVec
	resultChannelDropped *prometheus.CounterVec

	walletSyncTotal *prometheus.CounterVec

	instructionsReceived    prometheus.Counter
	instructionsRejected    *prometheus.CounterVec
	votingsStarted          prometheus.Counter
	votingThresholdDuration prometheus.Histogram
	finalizedEnqueueFail    prometheus.Counter
	finalizedActionLost     *prometheus.CounterVec

	actionDequeued         *prometheus.CounterVec
	actionEnqueued         *prometheus.CounterVec
	queueDepthReadFailures *prometheus.CounterVec

	infoRefreshFailures *prometheus.CounterVec
	infoRefreshDuration *prometheus.HistogramVec
	attestationVerify   *prometheus.CounterVec
	attestationPosture  *prometheus.GaugeVec

	nodeWaitDuration *prometheus.HistogramVec
	nodeWaitTotal    *prometheus.CounterVec

	machinepathPollTotal *prometheus.CounterVec

	policyEpoch     prometheus.Gauge
	policyLastFetch prometheus.Gauge
	policyUpdate    *prometheus.CounterVec

	ready prometheus.Gauge
}

var _ storage.Observer = (*Metrics)(nil)

// New builds the registry and registers the collectors for the enabled groups.
// With Enable false it returns a disabled Metrics over an empty registry.
func New(cfg Config) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{cfg: cfg, reg: reg}

	if !cfg.Enable {
		return m
	}

	f := promauto.With(reg)

	if cfg.Runtime {
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			buildInfo(),
		)
	}

	if cfg.HTTP {
		m.httpRequests = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_total",
			Help: "HTTP requests by server, route, and status class.",
		}, []string{"server", "route", "status_class"})
		m.httpDuration = f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help: "HTTP request handling latency by server and route.", Buckets: httpBuckets,
		}, []string{"server", "route"})
	}

	if cfg.Storage {
		m.storageOps = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "storage", Name: "operations_total",
			Help: "Storage operations by backend, namespace, operation, and outcome.",
		}, []string{"backend", "namespace", "operation", "outcome"})
		m.storageDuration = f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "storage", Name: "operation_duration_seconds",
			Help: "Storage operation latency by backend, namespace, and operation.", Buckets: storageBuckets,
		}, []string{"backend", "namespace", "operation"})
	}

	if cfg.Result {
		m.resultsProcessed = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "results_processed_total",
			Help: "Results processed by op command and status class.",
		}, []string{"op_command", "status_class"})
		m.resultsLost = f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "results_lost_total",
			Help: "Results acknowledged to the node but never persisted.",
		})
		m.resultsDiscarded = f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "results_discarded_total",
			Help: "Node delivery-failure notifications discarded for lacking an action ID.",
		})
		m.resultsRejected = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "results_rejected_total",
			Help: "Results rejected before storage, by reason.",
		}, []string{"reason"})
		// Pre-initialize so a first, possibly one-shot rejection satisfies increase()>0 (a series born at 1 never does).
		for _, reason := range []string{"bad_signer", "wrong_tee_id", "bootstrap"} {
			m.resultsRejected.WithLabelValues(reason).Add(0)
		}
		m.resultChannelDropped = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "result_channel_dropped_total",
			Help: "Result fan-out messages dropped because the target channel was full, by channel.",
		}, []string{"channel"})
	}

	if cfg.Wallet {
		m.walletSyncTotal = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "wallet", Name: "sync_total",
			Help: "Wallet key/proof sync cycles by result.",
		}, []string{"result"})
	}

	if cfg.Voting {
		m.instructionsReceived = f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "instructions_received_total",
			Help: "Instructions submitted.",
		})
		m.instructionsRejected = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "instructions_rejected_total",
			Help: "Instructions rejected, by reason.",
		}, []string{"reason"})
		m.votingsStarted = f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "votings_started_total",
			Help: "Votings opened (a new proposal box created).",
		})
		// voting_threshold_duration_seconds_count is the count of finalizations (votings that
		// reached threshold), so a separate consensus_reached_total counter is not needed.
		// It is intentionally unlabeled: consensus latency is governed by a protocol uniform
		// across op types, so an op_command label would stratify by traffic mix while
		// multiplying series for no diagnostic gain.
		m.votingThresholdDuration = f.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "voting_threshold_duration_seconds",
			Help: "Seconds from voting start to reaching threshold.", Buckets: votingDurationBuckets,
		})
		m.finalizedEnqueueFail = f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "finalized_action_enqueue_failed_total",
			Help: "Finalized actions that failed to enqueue to the main queue.",
		})
		m.finalizedActionLost = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "finalized_action_lost_total",
			Help: "Finalized actions dropped before enqueue, by reason.",
		}, []string{"reason"})
		// Pre-initialize so a first, possibly one-shot drop satisfies increase()>0 (a series born at 1 never
		// does); build_error feeds the TeeProxyFinalizedActionLost alert over a practically unreachable path.
		for _, reason := range []string{"build_error", "send_cancelled"} {
			m.finalizedActionLost.WithLabelValues(reason).Add(0)
		}
	}

	if cfg.Queue {
		m.actionDequeued = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "action_dequeue_total",
			Help: "Action dequeue attempts by queue and result: success returned a body; empty found nothing queued; action_not_found and error consumed a queue ID whose body could not be fetched (an orphaned/lost action).",
		}, []string{"queue", "result"})
		m.actionEnqueued = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "action_enqueue_total",
			Help: "Action enqueue attempts by queue and result.",
		}, []string{"queue", "result"})
		m.queueDepthReadFailures = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "action_queue_depth_read_failures_total",
			Help: "Scrape-time queue-depth (LLEN) read failures by queue; while nonzero the depth gauge reports 0 and the backpressure alert is masked.",
		}, []string{"queue"})
	}

	if cfg.Info {
		m.infoRefreshFailures = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "info_refresh_failures_total",
			Help: "TEE info refresh failures by pipeline stage.",
		}, []string{"stage"})
		// Pre-initialize so a first, possibly one-shot failure satisfies increase()>0 (a series born at 1 never does).
		for _, stage := range []string{
			"fetch_block", "create_action", "enqueue", "wait_response", "action_status",
			"unmarshal", "parse_tee_id", "signing_hash", "verify_signature", "verify_attestation",
		} {
			m.infoRefreshFailures.WithLabelValues(stage).Add(0)
		}
		m.infoRefreshDuration = f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "info_refresh_duration_seconds",
			Help: "End-to-end TEE info refresh latency by outcome.", Buckets: infoRefreshBuckets,
		}, []string{"result"})
	}

	if cfg.Attestation {
		m.attestationVerify = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "attestation_verify_total",
			Help: "Attestation verification attempts by result and reason.",
		}, []string{"result", "reason"})
		// Pre-initialize so a first, possibly one-shot verification event satisfies increase()>0 (a series born at 1 never does).
		// The ok/magic_pass series is pre-initialized too so the critical, no-for: TeeProxyMagicPassAccepted fires on the first accepted sentinel.
		m.attestationVerify.WithLabelValues("ok", "ok").Add(0)
		m.attestationVerify.WithLabelValues("ok", attestation.ReasonMagicPass).Add(0)
		for _, reason := range attestation.ErrorReasons {
			m.attestationVerify.WithLabelValues("error", reason).Add(0)
		}
		m.attestationPosture = f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "attestation_posture",
			Help: "Attestation verification posture: 1 if the named setting is active, else 0.",
		}, []string{"setting"})
	}

	if cfg.Node {
		m.nodeWaitDuration = f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "node", Name: "response_wait_duration_seconds",
			Help: "Synchronous wait for a TEE-node response, by path.", Buckets: nodeBuckets,
		}, []string{"path"})
		m.nodeWaitTotal = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "node", Name: "response_wait_total",
			Help: "TEE-node response waits by path and outcome.",
		}, []string{"path", "result"})
		m.machinepathPollTotal = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "machinepath", Name: "poll_total",
			Help: "Machine-path poll cycles by result; excludes node-wait and status outcomes already covered by node_response_wait_total and results_processed_total.",
		}, []string{"result"})
	}

	if cfg.Policy {
		m.policyEpoch = f.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "policy_active_reward_epoch",
			Help: "Reward epoch of the active signing policy.",
		})
		m.policyLastFetch = f.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "policy_last_fetch_timestamp_seconds",
			Help: "Unix time of the last error-free signing-policy update-loop iteration.",
		})
		m.policyUpdate = f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "policy_update_total",
			Help: "Signing-policy update-loop iterations by outcome.",
		}, []string{"result"})
	}

	if cfg.Liveness {
		m.ready = f.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "ready",
			Help: "1 if the last readiness check passed, else 0.",
		})
	}

	return m
}

// Enabled reports whether metrics collection and the /metrics endpoint are active.
func (m *Metrics) Enabled() bool {
	return m != nil && m.cfg.Enable
}

// Registry returns the underlying registry for mounting on an HTTP handler, or nil
// if metrics are disabled.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// HTTPEnabled reports whether per-request HTTP metrics are collected.
func (m *Metrics) HTTPEnabled() bool {
	return m != nil && m.httpRequests != nil
}

// ObserveHTTP records one served request. An empty route is reported as "unmatched".
func (m *Metrics) ObserveHTTP(server, route string, status int, d time.Duration) {
	if m == nil || m.httpRequests == nil {
		return
	}
	if route == "" {
		route = "unmatched"
	}
	m.httpRequests.WithLabelValues(server, route, statusClass(status)).Inc()
	m.httpDuration.WithLabelValues(server, route).Observe(d.Seconds())
}

// StorageObserver returns an observer for the storage layer, or nil when storage
// metrics are disabled (so the decorator is skipped entirely).
func (m *Metrics) StorageObserver() storage.Observer {
	if m == nil || m.storageOps == nil {
		return nil
	}
	return m
}

// Observe records one storage operation; it satisfies storage.Observer.
func (m *Metrics) Observe(backend, ns, operation, outcome string, d time.Duration) {
	if m == nil || m.storageOps == nil {
		return
	}
	m.storageOps.WithLabelValues(backend, ns, operation, outcome).Inc()
	m.storageDuration.WithLabelValues(backend, ns, operation).Observe(d.Seconds())
}

// InstructionReceived records one submitted instruction.
func (m *Metrics) InstructionReceived() {
	if m == nil || m.instructionsReceived == nil {
		return
	}
	m.instructionsReceived.Inc()
}

// InstructionRejected records one rejected instruction under a bounded reason.
func (m *Metrics) InstructionRejected(reason string) {
	if m == nil || m.instructionsRejected == nil {
		return
	}
	m.instructionsRejected.WithLabelValues(reason).Inc()
}

// VotingStarted records that a new voting (proposal box) was opened.
func (m *Metrics) VotingStarted() {
	if m == nil || m.votingsStarted == nil {
		return
	}
	m.votingsStarted.Inc()
}

// VotingThresholdReached records the time from voting start to reaching threshold.
func (m *Metrics) VotingThresholdReached(d time.Duration) {
	if m == nil || m.votingThresholdDuration == nil {
		return
	}
	m.votingThresholdDuration.Observe(d.Seconds())
}

// FinalizedActionEnqueueFailed records a finalized action that failed to enqueue.
func (m *Metrics) FinalizedActionEnqueueFailed() {
	if m == nil || m.finalizedEnqueueFail == nil {
		return
	}
	m.finalizedEnqueueFail.Inc()
}

// FinalizedActionLost records a finalized action dropped before the main-queue enqueue,
// by bounded reason ("build_error", "send_cancelled"). Callers pass compile-time constants,
// so the reason enum stays closed. It is a sibling of FinalizedActionEnqueueFailed, which
// keeps counting enqueue failures separately.
func (m *Metrics) FinalizedActionLost(reason string) {
	if m == nil || m.finalizedActionLost == nil {
		return
	}
	m.finalizedActionLost.WithLabelValues(reason).Inc()
}

// ActiveVotersEnabled reports whether the distinct-voter gauge is collected.
func (m *Metrics) ActiveVotersEnabled() bool {
	return m != nil && m.cfg.Enable && m.cfg.ActiveVoters
}

// RegisterActiveInitiators registers a scrape-time gauge of count(), the distinct voting
// initiators (proposers) in the current reward epoch. No-op when active-voters is disabled.
func (m *Metrics) RegisterActiveInitiators(count func() float64) {
	if !m.ActiveVotersEnabled() || count == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Name: "active_initiators",
		Help: "Distinct initiators (proposers) that opened at least one voting in the current reward epoch.",
	}, count))
}

// RegisterActiveDataProviderVoters registers a scrape-time gauge of count(), the distinct
// data-provider voters in the current reward epoch. No-op when active-voters is disabled.
func (m *Metrics) RegisterActiveDataProviderVoters(count func() float64) {
	if !m.ActiveVotersEnabled() || count == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Name: "active_data_provider_voters",
		Help: "Distinct data-provider voters that cast at least one accepted vote in the current reward epoch.",
	}, count))
}

// ProviderPending pairs a provider address with its count of unfinalized proposals.
type ProviderPending struct {
	Provider string
	Pending  float64
}

// topPendingCollector emits one gauge sample per provider returned by top() at scrape time.
// Building the samples fresh each scrape means only the current top providers appear and no
// stale per-address series accumulate in the registry.
type topPendingCollector struct {
	desc *prometheus.Desc
	top  func() []ProviderPending
}

func (c topPendingCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c topPendingCollector) Collect(ch chan<- prometheus.Metric) {
	for _, p := range c.top() {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, p.Pending, p.Provider)
	}
}

// RegisterTopUnfinalizedProposals registers a scrape-time collector that emits, per provider,
// the number of unfinalized proposals it holds — for the providers returned by top() (intended
// to be the top 3), omitting any with none. No-op when active-voters is disabled.
func (m *Metrics) RegisterTopUnfinalizedProposals(top func() []ProviderPending) {
	if !m.ActiveVotersEnabled() || top == nil {
		return
	}
	m.reg.MustRegister(topPendingCollector{
		desc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "top_provider_unfinalized_proposals"),
			"Unfinalized proposals held by each of the top providers (top 3) in the current reward epoch; providers with none are omitted.",
			[]string{"provider"}, nil,
		),
		top: top,
	})
}

// InfoRefreshFailed records a TEE info refresh failure at the given pipeline stage.
func (m *Metrics) InfoRefreshFailed(stage string) {
	if m == nil || m.infoRefreshFailures == nil {
		return
	}
	m.infoRefreshFailures.WithLabelValues(stage).Inc()
}

// InfoRefreshObserved records one completed TEE-info refresh by duration and outcome
// ("ok"/"error"). It gives the failure counter a denominator; the per-stage breakdown
// stays in info_refresh_failures_total.
func (m *Metrics) InfoRefreshObserved(d time.Duration, err error) {
	if m == nil || m.infoRefreshDuration == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.infoRefreshDuration.WithLabelValues(result).Observe(d.Seconds())
}

// AttestationVerified records one attestation verification by result ("ok"/"error")
// and bounded reason.
func (m *Metrics) AttestationVerified(result, reason string) {
	if m == nil || m.attestationVerify == nil {
		return
	}
	m.attestationVerify.WithLabelValues(result, reason).Inc()
}

// SetAttestationPosture records the attestation verification posture, setting the
// attestation_posture gauge to 1 for each active setting and 0 otherwise. The setting
// key set is closed — the sole caller passes a fixed-key literal map: enabled,
// magic_pass_allowed, audience_check, code_hash_check, platform_check, debug_status_check,
// max_token_age_check, sec_boot_check.
func (m *Metrics) SetAttestationPosture(settings map[string]bool) {
	if m == nil || m.attestationPosture == nil {
		return
	}
	for k, on := range settings {
		v := 0.0
		if on {
			v = 1
		}
		m.attestationPosture.WithLabelValues(k).Set(v)
	}
}

// ObserveNodeWait records one synchronous wait for a TEE-node response on the given
// path ("info"/"machinepath"/"wallet_key_info"/"wallet_key_proof"/"policy_update"),
// classifying the outcome from err ("ok"/"timeout"/"cancelled"/"error").
func (m *Metrics) ObserveNodeWait(path string, d time.Duration, err error) {
	if m == nil || m.nodeWaitDuration == nil {
		return
	}
	m.nodeWaitDuration.WithLabelValues(path).Observe(d.Seconds())
	m.nodeWaitTotal.WithLabelValues(path, nodeWaitResult(err)).Inc()
}

// nodeWaitResult maps a WaitOnResponse error to a bounded outcome label. A pubsub wait
// expiry surfaces as a net read-deadline error (*net.OpError wrapping os.ErrDeadlineExceeded)
// rather than context.DeadlineExceeded — go-redis turns the context deadline into a raw
// connection deadline — so net timeouts classify as "timeout" too.
func nodeWaitResult(err error) string {
	var netErr net.Error
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timeout"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "error"
	}
}

// MachinepathPollObserved records one machine-path poll-loop outcome under a bounded result
// ("fetch_error"/"build_error"/"enqueue_error"/"no_change"/"confirmed"). Wait and
// status-rejection outcomes are already covered by node_response_wait_total and
// results_processed_total and are intentionally not duplicated here.
func (m *Metrics) MachinepathPollObserved(result string) {
	if m == nil || m.machinepathPollTotal == nil {
		return
	}
	m.machinepathPollTotal.WithLabelValues(result).Inc()
}

// SetActiveRewardEpoch records the reward epoch of the active signing policy.
func (m *Metrics) SetActiveRewardEpoch(epoch uint32) {
	if m == nil || m.policyEpoch == nil {
		return
	}
	m.policyEpoch.Set(float64(epoch))
}

// SetPolicyFetched records that the signing-policy update loop completed an error-free
// iteration, refreshing the last-fetch timestamp used to detect a wedged loop.
func (m *Metrics) SetPolicyFetched() {
	if m == nil || m.policyLastFetch == nil {
		return
	}
	m.policyLastFetch.SetToCurrentTime()
}

// PolicyUpdate records one signing-policy update-loop iteration under a bounded result
// ("empty"/"fetch_error"/"reconciled"/"reconcile_error"/"build_failed"/"enqueue_failed"/
// "await_failed"/"rejected"/"applied").
func (m *Metrics) PolicyUpdate(result string) {
	if m == nil || m.policyUpdate == nil {
		return
	}
	m.policyUpdate.WithLabelValues(result).Inc()
}

// PolicyEnabled reports whether signing-policy metrics are collected.
func (m *Metrics) PolicyEnabled() bool {
	return m != nil && m.cfg.Enable && m.cfg.Policy
}

// RegisterOldestStoredPolicy registers a scrape-time gauge reporting the oldest reward epoch
// with a signing policy still resident in the in-memory voting window. The newest/active epoch
// is already exposed by policy_active_reward_epoch. No-op when the policy group is disabled.
func (m *Metrics) RegisterOldestStoredPolicy(oldest func() float64) {
	if !m.PolicyEnabled() || oldest == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Name: "signing_policy_oldest_reward_epoch",
		Help: "Oldest reward epoch with a signing policy still resident in the in-memory voting window.",
	}, oldest))
}

// LivenessEnabled reports whether liveness metrics are collected.
func (m *Metrics) LivenessEnabled() bool {
	return m != nil && m.cfg.Enable && m.cfg.Liveness
}

// SetReady records the outcome of the latest readiness check.
func (m *Metrics) SetReady(ready bool) {
	if m == nil || m.ready == nil {
		return
	}
	if ready {
		m.ready.Set(1)
		return
	}
	m.ready.Set(0)
}

// RegisterInfoDelay registers a gauge reporting seconds() at scrape time. It is a
// no-op when the liveness group is disabled, so seconds is never invoked.
func (m *Metrics) RegisterInfoDelay(seconds func() float64) {
	if !m.LivenessEnabled() || seconds == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Name: "info_service_delay_seconds",
		Help: "Seconds since the last successful TEE info refresh.",
	}, seconds))
}

// QueueEnabled reports whether queue metrics are collected.
func (m *Metrics) QueueEnabled() bool {
	return m != nil && m.cfg.Enable && m.cfg.Queue
}

// ActionDequeued records one dequeue attempt by queue and result.
func (m *Metrics) ActionDequeued(queue, result string) {
	if m == nil || m.actionDequeued == nil {
		return
	}
	m.actionDequeued.WithLabelValues(queue, result).Inc()
}

// ActionEnqueued records one enqueue attempt by queue and result.
func (m *Metrics) ActionEnqueued(queue, result string) {
	if m == nil || m.actionEnqueued == nil {
		return
	}
	m.actionEnqueued.WithLabelValues(queue, result).Inc()
}

// QueueDepthReadFailed records a scrape-time queue-depth (LLEN) read failure for the given queue.
func (m *Metrics) QueueDepthReadFailed(queue string) {
	if m == nil || m.queueDepthReadFailures == nil {
		return
	}
	m.queueDepthReadFailures.WithLabelValues(queue).Inc()
}

// RegisterQueueDepth registers a gauge reporting depth() for the given queue at scrape
// time. It is a no-op when the queue group is disabled, so depth is never invoked.
func (m *Metrics) RegisterQueueDepth(queue string, depth func() float64) {
	if !m.QueueEnabled() || depth == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Name: "action_queue_depth",
		Help:        "Pending submission IDs per queue.",
		ConstLabels: prometheus.Labels{"queue": queue},
	}, depth))
}

// WalletEnabled reports whether wallet key/proof sync-cycle metrics are collected.
func (m *Metrics) WalletEnabled() bool {
	return m != nil && m.cfg.Enable && m.cfg.Wallet
}

// WalletSyncObserved records one wallet key/proof sync-cycle outcome under a bounded
// result ("success"/"enqueue_error"/"parse_error"/"skipped").
func (m *Metrics) WalletSyncObserved(result string) {
	if m == nil || m.walletSyncTotal == nil {
		return
	}
	m.walletSyncTotal.WithLabelValues(result).Inc()
}

// RegisterWalletKeysCached registers a scrape-time gauge reporting count(), the key proofs
// cached in the wallet service's in-memory store. No-op when the wallet group is disabled.
func (m *Metrics) RegisterWalletKeysCached(count func() float64) {
	if !m.WalletEnabled() || count == nil {
		return
	}
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "wallet", Name: "keys_cached",
		Help: "Key proofs cached in the wallet service's in-memory store.",
	}, count))
}

// ResultProcessed records one validated TEE-node result delivery by op command and status
// class. It is counted after the identity gates but before storage, so a re-delivery that the
// storage override-guard later rejects (a duplicate of an already-persisted result) still
// counts here: it measures processed deliveries, not distinct stored results.
func (m *Metrics) ResultProcessed(opCommand common.Hash, status uint8) {
	if m == nil || m.resultsProcessed == nil {
		return
	}
	m.resultsProcessed.WithLabelValues(opCommandLabel(opCommand), resultStatusClass(status)).Inc()
}

// ResultLost records a result that was acknowledged to the node but not persisted.
func (m *Metrics) ResultLost() {
	if m == nil || m.resultsLost == nil {
		return
	}
	m.resultsLost.Inc()
}

// ResultDiscarded records a node delivery-failure notification dropped for lacking an action ID.
func (m *Metrics) ResultDiscarded() {
	if m == nil || m.resultsDiscarded == nil {
		return
	}
	m.resultsDiscarded.Inc()
}

// ResultRejected records one result rejected before storage under a bounded reason
// ("bad_signer", "wrong_tee_id", "bootstrap").
func (m *Metrics) ResultRejected(reason string) {
	if m == nil || m.resultsRejected == nil {
		return
	}
	m.resultsRejected.WithLabelValues(reason).Inc()
}

// ResultChannelDropped records one message dropped from a full result fan-out channel
// under a bounded channel name ("key_actions", "backups", "backup_trigger").
func (m *Metrics) ResultChannelDropped(channel string) {
	if m == nil || m.resultChannelDropped == nil {
		return
	}
	m.resultChannelDropped.WithLabelValues(channel).Inc()
}

// resultStatusClass maps a TEE result status to a bounded class label.
func resultStatusClass(status uint8) string {
	switch status {
	case 0:
		return "failed"
	case 1:
		return "final"
	default:
		return "transient"
	}
}

// statusClass maps an HTTP status code to its class label (2xx, 4xx, …).
func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// buildInfo is a constant 1-valued gauge labeled with the build's VCS revision and
// Go version, read from the embedded build info ("unknown" when not VCS-stamped).
func buildInfo() prometheus.Collector {
	// An -ldflags-injected revision (container builds, where .git is absent) takes
	// precedence; otherwise fall back to the vcs.revision build setting (plain go build).
	revision, goVersion := version.Revision, "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		goVersion = bi.GoVersion
		if revision == "unknown" {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					revision = s.Value
				}
			}
		}
	}

	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Name:        "build_info",
		Help:        "Constant 1, labeled with build metadata (version, VCS revision, and Go version).",
		ConstLabels: prometheus.Labels{"version": version.Version, "revision": revision, "go_version": goVersion},
	})
	g.Set(1)

	return g
}
