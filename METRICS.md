# Metrics

Prometheus metrics exposed by the TEE proxy.

All metrics are opt-in.
Nothing is collected and the `/metrics` endpoint is not mounted unless `[metrics] enable = true`.
The endpoint is served on `GET /metrics` of the internal server (port `6661` by default; it follows `ports.internal`) and inherits that port's trust model.
The handler negotiates the OpenMetrics exposition format, caps concurrent scrapes, and logs any gather/encode error.

Set the Prometheus `scrape_timeout` to at least 10s.
When the `queue` group is enabled, a scrape reads each action queue's depth with a Redis `LLEN` bounded by a 2s timeout, and the three reads run serially during collection, so a fully unreachable Redis can stretch a single scrape to roughly 6s.
This delays only the scrape — no request-handling path holds a lock across that read — but a tighter `scrape_timeout` would mark scrapes failed during a Redis incident.

Every metric name is prefixed with `teeproxy_` (except the standard `go_*` and `process_*` runtime collectors).
Collection is split into groups that can be toggled independently; an omitted group inherits `enable`, and setting a group to `false` omits just that group.
Gauges marked **scrape-time** are computed live when `/metrics` is scraped.

## Reading these metrics

Counters (`*_total`) are cumulative from process start and reset only on restart, so graph them with `rate()` or `increase()` rather than reading the raw value.
Gauges — including the scrape-time ones — hold a current value and are read directly.
For the histograms, use `histogram_quantile(0.95, rate(<name>_bucket[5m]))` for a latency quantile and `rate(<name>_sum[5m]) / rate(<name>_count[5m])` for the mean; `<name>_count` is itself the cumulative count of observed events.

| Group | Enables |
| --- | --- |
| `http` | per-request count and latency |
| `storage` | Redis/Firestore operation count, latency, and errors |
| `queue` | dequeue counters and queue-depth gauge |
| `voting` | instruction and votings-started counters, threshold-duration histogram |
| `active_voters` | per-epoch participant gauges |
| `result` | result throughput, lost, discarded, and rejected counters |
| `info` | TEE info refresh duration and per-stage failures |
| `attestation` | attestation verify outcomes |
| `policy` | active and resident signing-policy reward-epoch gauges |
| `liveness` | readiness gauge and info-staleness gauge |
| `node` | TEE-node response-wait latency and outcomes |
| `runtime` | Go runtime/process collectors and build info |

## Alerting

Example Prometheus alerting rules live in [`examples/monitoring/alerts.yaml`](examples/monitoring/alerts.yaml) and are syntax-checked in CI (`promtool check rules`).
They are starting points: thresholds mirror the proxy's in-code constants but the right values depend on your traffic and SLOs.
The expressions carry no scrape-job selector, so add your own (e.g. `{job="tee-proxy"}`), and each rule needs its metric group enabled.

Page-now (critical) signals:

| Alert | Condition | Why it pages |
| --- | --- | --- |
| `TeeProxyResultsLost` | `increase(teeproxy_results_lost_total[5m]) > 0` | Irrecoverable, client-invisible result loss. |
| `TeeProxyResultWrongTeeID` | `increase(teeproxy_results_rejected_total{reason="wrong_tee_id"}[5m]) > 0` | A result signed by a key other than the bound TEE identity — tamper / mis-route. |
| `TeeProxyAttestationFailing` | `increase(teeproxy_attestation_verify_total{result="error"}[10m]) > 0` | Attestation verification failed — possible compromise. |
| `TeeProxyFinalizedActionEnqueueFailing` | `increase(teeproxy_finalized_action_enqueue_failed_total[5m]) > 0` | Consensus reached but the action was dropped. |
| `TeeProxyInfoStale` | `teeproxy_info_service_delay_seconds > 140` | Past the 140s readiness tolerance (`liveness.go` `infoDelayTolerance`). |
| `TeeProxyNotReady` | `teeproxy_ready == 0` for 2m | Readiness failing. |

Warnings (lead time before a page):

| Alert | Condition | Signal |
| --- | --- | --- |
| `TeeProxyInfoStaleWarning` | `teeproxy_info_service_delay_seconds > 70` | Half the 140s tolerance — lead time before readiness flips. |
| `TeeProxyNodeWaitTimeouts` | `rate(teeproxy_node_response_wait_total{result="timeout"}[10m]) > 0` | TEE node slow or unreachable on a path. |
| `TeeProxyQueueBackpressure` | `teeproxy_action_queue_depth{queue="main"} > 100` for 10m | Node not draining (`queue.go` `queueDepthWarnThreshold`). |
| `TeeProxyConsensusStall` | votings started but none finalized in 15m | Offline voters / mis-set threshold / partition. |
| `TeeProxyHigh5xxRate` | 5xx ratio > 5% for 10m | Edge errors on a server. |
| `TeeProxyStorageErrors` | `rate(teeproxy_storage_operations_total{outcome="error"}[5m]) > 0` | Redis/Firestore trouble. |

## `http`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_http_requests_total` | counter | `server`, `route`, `status_class` | HTTP requests by server, route, and status class. |
| `teeproxy_http_request_duration_seconds` | histogram | `server`, `route` | HTTP request handling latency by server and route. |

Label values: `server` is `internal` or `external`; `route` is the matched mux route template (e.g. `POST /queue/{queueID}`) or `unmatched`; `status_class` is `1xx`/`2xx`/`3xx`/`4xx`/`5xx`.

## `storage`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_storage_operations_total` | counter | `backend`, `namespace`, `operation`, `outcome` | Storage operations by backend, namespace, operation, and outcome. |
| `teeproxy_storage_operation_duration_seconds` | histogram | `backend`, `namespace`, `operation` | Storage operation latency by backend, namespace, and operation. |

Label values: `backend` is `redis` or `firestore`; `namespace` is `results`/`backups`/`backupIndex`; `operation` is `set`/`set_with_ttl`/`get`/`remove`; `outcome` is `success`/`not_found`/`empty_queue`/`error`.

## `queue`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_action_dequeue_total` | counter | `queue`, `result` | Action dequeue attempts by queue and result. A nonempty dequeue is `result="success"`. |
| `teeproxy_action_queue_depth` | gauge (scrape-time) | `queue` | Pending submission IDs per queue. |

Label values: `queue` is `main`/`direct`/`backup`; `result` is `success`/`empty`/`error`/`action_not_found`.

## `voting`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_instructions_received_total` | counter | — | Instructions submitted. |
| `teeproxy_instructions_rejected_total` | counter | `reason` | Instructions rejected, by reason. |
| `teeproxy_votings_started_total` | counter | — | Votings opened (a new proposal box created). |
| `teeproxy_voting_threshold_duration_seconds` | histogram | — | Seconds from voting start to reaching threshold. Its `_count` is the number of votings finalized. |
| `teeproxy_finalized_action_enqueue_failed_total` | counter | — | Finalized actions that failed to enqueue to the main queue. |

Label values: `reason` is one of `wrong_tee_id`/`invalid_op`/`invalid_signature`/`invalid_voter`/`voting_ended`/`duplicate_signature`/`event_in_future`/`other`.
A voting is finalized exactly when it reaches threshold, so "votings finalized" is `voting_threshold_duration_seconds_count` (the histogram's observation count).
The threshold-duration histogram is intentionally unlabeled: consensus latency is governed by a protocol uniform across op types, so an `op_command` label would only stratify it by traffic mix.
Every started voting eventually either finalizes or expires, so expired votings are `votings_started_total − voting_threshold_duration_seconds_count` (exactly once all in-flight votings have closed; instantaneously this also includes votings still open within the proposal-expiration window).

## `active_voters`

All gauges are per current reward epoch and scrape-time.

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_active_data_provider_voters` | gauge (scrape-time) | — | Distinct data-provider voters (policy-registered, weight-bearing) that cast at least one accepted vote in the current reward epoch. |
| `teeproxy_active_initiators` | gauge (scrape-time) | — | Distinct initiators (proposers) that opened at least one voting in the current reward epoch. |
| `teeproxy_top_provider_unfinalized_proposals` | gauge (scrape-time) | `provider` | Unfinalized proposals held by each of the top 3 providers (by count) in the current reward epoch; providers with none are omitted, so the metric has no series when all are zero. |

The `provider` label on `top_provider_unfinalized_proposals` is a voter address.
It is bounded to at most 3 series per scrape, but the set of addresses that appear changes over time as different providers enter the top 3 (the usual cost of an address-valued label).

## `result`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_results_processed_total` | counter | `op_command`, `status_class` | Results processed by op command and status class. |
| `teeproxy_results_lost_total` | counter | — | Results acknowledged to the node but never persisted. |
| `teeproxy_results_discarded_total` | counter | — | Node delivery-failure notifications discarded for lacking an action ID. |
| `teeproxy_results_rejected_total` | counter | `reason` | Results rejected before storage, by reason. |

Label values: `op_command` is a bounded operation-command name, else `other`; `status_class` is `failed`/`final`/`transient`; `reason` is `bad_signer`/`wrong_tee_id`/`bootstrap`.
`results_processed_total` is counted after the identity gates but before storage, so a re-delivery that the storage override-guard later rejects (a duplicate of an already-persisted result) still counts here — it measures processed deliveries, not distinct stored results.
A `wrong_tee_id` rejection means a result was signed by a key other than the bound TEE identity (a tamper / mis-route signal); `bad_signer` is a malformed signature; `bootstrap` is a non-TEE_INFO result arriving before the identity is set (an expected startup transient).

## `info`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_info_refresh_failures_total` | counter | `stage` | TEE info refresh failures by pipeline stage. |
| `teeproxy_info_refresh_duration_seconds` | histogram | `result` | End-to-end TEE info refresh latency by outcome. |

Label values: `stage` is one of the refresh-pipeline stages (`fetch_block`, `create_action`, `enqueue`, `wait_response`, `action_status`, `unmarshal`, `parse_tee_id`, `signing_hash`, `verify_signature`, `verify_attestation`); `result` is `ok`/`error`.
The duration histogram is observed once per refresh, so its `_count{result}` is the refresh rate and success ratio — the denominator the per-stage failure counter lacks — and its buckets capture the TEE round-trip latency.

## `attestation`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_attestation_verify_total` | counter | `result`, `reason` | Attestation verification attempts by result and reason. |

Label values: `result` is `ok` or `error`; `reason` is a bounded reason (`ok`, `other`, or a mapped verification-failure reason).

## `policy`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_policy_active_reward_epoch` | gauge | — | Reward epoch of the active signing policy, which is also the newest one the proxy holds. |
| `teeproxy_signing_policy_oldest_reward_epoch` | gauge (scrape-time) | — | Oldest reward epoch with a signing policy still resident in the in-memory voting window. |

The proxy does not persist signing policies.
The active policy (`policy_active_reward_epoch`) is the newest one it has ingested, so the resident window runs from `signing_policy_oldest_reward_epoch` up to `policy_active_reward_epoch`.
The window is the voting cyclic buffer, whose size is `voting.history_size` (default 3).

## `liveness`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_ready` | gauge | — | 1 if the last readiness check passed, else 0. |
| `teeproxy_info_service_delay_seconds` | gauge (scrape-time) | — | Seconds since the last successful TEE info refresh. |

## `node`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_node_response_wait_duration_seconds` | histogram | `path` | Synchronous wait for a TEE-node response, by path. |
| `teeproxy_node_response_wait_total` | counter | `path`, `result` | TEE-node response waits by path and outcome. |

Label values: `path` is `info`/`machinepath`/`wallet_key_info`/`wallet_key_proof`; `result` is `ok`/`timeout`/`cancelled`/`error`.
This is the proxy's synchronous round-trip to the TEE node (the wait inside `WaitOnResponse`).
A rising `timeout` share, or a p99 approaching the per-path response timeout (2–3 minutes), is the leading signal that the node is slow or unreachable — and the `path` label localizes partial degradation (e.g. `wallet_key_proof` slow while `info` is fine).

## `runtime`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_build_info` | gauge | `version`, `revision`, `go_version` | Constant 1, labeled with build metadata (version, VCS revision, and Go version). |
| `go_*` | various | — | Standard Go runtime collector (includes `go_goroutines`, GC stats, etc.). |
| `process_*` | various | — | Standard process collector (CPU, memory, file descriptors, etc.). |
