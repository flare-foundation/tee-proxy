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
| `queue` | enqueue and dequeue counters, queue-depth gauge, and depth-read failure counter |
| `voting` | instruction and votings-started counters, threshold-duration histogram |
| `active_voters` | per-epoch participant gauges |
| `result` | result throughput, lost, discarded, and rejected counters |
| `wallet` | wallet sync-cycle outcome counter and cached-key gauge |
| `info` | TEE info refresh duration and per-stage failures |
| `attestation` | attestation verify outcomes |
| `policy` | active/resident reward-epoch gauges, fetch-loop staleness timestamp, and update-outcome counter |
| `liveness` | readiness gauge and info-staleness gauge |
| `node` | TEE-node response-wait latency and outcomes; machine-path poll-cycle outcomes |
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
| `TeeProxyInfoVerifySignatureFailing` | `increase(teeproxy_info_refresh_failures_total{stage="verify_signature"}[5m]) > 0` | The TEE_INFO response failed signature verification — the info-path sibling of `TeeProxyResultWrongTeeID`. |
| `TeeProxyAttestationFailing` | `increase(teeproxy_attestation_verify_total{result="error"}[10m]) > 0` | Attestation verification failed — possible compromise. |
| `TeeProxyMagicPassAccepted` | `increase(teeproxy_attestation_verify_total{reason="magic_pass"}[10m]) > 0` | A TEE_INFO response was accepted via the magic_pass sentinel instead of a real JWT chain (`result="ok"`, so not covered by `TeeProxyAttestationFailing`). |
| `TeeProxyMagicPassAllowed` | `teeproxy_attestation_posture{setting="magic_pass_allowed"} == 1` for 5m | Configured with `allow_magic_pass=true`, permitting the JWT chain to be bypassed — must not run in production. |
| `TeeProxyFinalizedActionEnqueueFailing` | `increase(teeproxy_finalized_action_enqueue_failed_total[5m]) > 0` | Consensus reached but the action failed to enqueue to the main queue (enqueue failures only; pre-enqueue drops are in `teeproxy_finalized_action_lost_total`). |
| `TeeProxyInfoStale` | `teeproxy_info_service_delay_seconds > 140` for 2m | Past the 140s readiness tolerance (`liveness.go` `infoDelayTolerance`); the 2m debounce absorbs the bootstrap window. |
| `TeeProxyNotReady` | `teeproxy_ready == 0` for 2m | Readiness failing. |
| `TeeProxyDown` | `up{job="tee-proxy"} == 0` for 2m | Prometheus cannot scrape the proxy. **Example only** — keys on Prometheus's synthetic `up` metric, not a `teeproxy_*` one; the `job` selector must match your scrape config. |

The `teeproxy_results_rejected_total`, `teeproxy_attestation_verify_total`, and `teeproxy_info_refresh_failures_total` series are pre-initialized to 0 at startup (when their group is enabled) so the first, possibly one-shot, event satisfies `increase(...) > 0`.
Any future counter feeding an `increase(...) > 0` alert must be pre-initialized the same way — a `CounterVec` series born at 1 never fires `increase > 0`.

Warnings (lead time before a page):

| Alert | Condition | Signal |
| --- | --- | --- |
| `TeeProxyInfoStaleWarning` | `teeproxy_info_service_delay_seconds > 70` for 1m | Half the 140s tolerance — lead time before readiness flips. |
| `TeeProxyNodeWaitTimeouts` | `sum by (path) (rate(teeproxy_node_response_wait_total{result="timeout"}[10m])) > 0` for 10m | TEE node slow or unreachable on a path. |
| `TeeProxyQueueBackpressure` | `teeproxy_action_queue_depth{queue="main"} > 100` for 10m | Main queue not draining (`queue.go` `queueDepthWarnThreshold`). |
| `TeeProxyQueueBackpressureDirect` | `teeproxy_action_queue_depth{queue="direct"} > 100` for 10m | Direct queue not draining (same threshold). |
| `TeeProxyQueueBackpressureBackup` | `teeproxy_action_queue_depth{queue="backup"} > 100` for 10m | Backup queue not draining (same threshold). |
| `TeeProxyActionEnqueueFailing` | `sum(rate(teeproxy_action_enqueue_total{result=~"store_error\|queue_error"}[5m])) > 0` for 10m | Actions failing to enqueue on the ingest path (Redis SET or LPUSH errors). |
| `TeeProxyQueueDepthReadFailing` | `sum(rate(teeproxy_action_queue_depth_read_failures_total[5m])) > 0` for 10m | Scrape-time queue-depth reads failing — the depth gauge reports 0 and the backpressure alerts are blind. |
| `TeeProxyFinalizedActionLost` | `increase(teeproxy_finalized_action_lost_total{reason="build_error"}[5m]) > 0` | A finalized action was dropped before enqueue because building it failed to serialize — a practically unreachable path. |
| `TeeProxyConsensusStall` | votings started but none finalized in 15m | Offline voters / mis-set threshold / partition. |
| `TeeProxyHigh5xxRate` | 5xx ratio > 5% for 10m | Edge errors on a server. |
| `TeeProxyStorageErrors` | `sum by (backend) (rate(teeproxy_storage_operations_total{outcome="error"}[5m])) > 0` for 10m | Redis/Firestore trouble. |
| `TeeProxyAttestationMetricsMissing` | `absent(teeproxy_attestation_verify_total)` for 15m | The `attestation` group is disabled, so the page-now `TeeProxyAttestationFailing` rule is inert. |
| `TeeProxyResultMetricsMissing` | `absent(teeproxy_results_rejected_total)` for 15m | The `result` group is disabled, so the page-now `TeeProxyResultWrongTeeID` rule is inert. |
| `TeeProxyPolicyFetchStalled` | `time() - teeproxy_policy_last_fetch_timestamp_seconds > 1800` for 5m | Update loop has not completed an error-free iteration in ~3x the fetch interval (default 10m). |
| `TeeProxyPolicyUpdateFailing` | `increase(teeproxy_policy_update_total{result=~"build_failed\|enqueue_failed\|await_failed\|rejected"}[15m]) > 3` for 15m | A rollover is repeatedly failing to build/enqueue/confirm/apply. |
| `TeeProxyWalletSyncFailing` | `increase(teeproxy_wallet_sync_total{result=~"enqueue_error\|parse_error"}[15m]) > 0` | A wallet key/proof sync cycle failed before or after the node wait. |
| `TeeProxyWalletSyncWedged` | `increase(teeproxy_wallet_sync_total{result="skipped"}[3h]) >= 3` | 3 consecutive skips at the 1h sync cadence means sync has not completed in 3+ hours — a wedged `syncing` flag. |
| `TeeProxyMachinepathPollErrors` | `increase(teeproxy_machinepath_poll_total{result=~"fetch_error\|build_error\|enqueue_error"}[15m]) > 0` | A machine-path poll cycle failed before the node ever saw the action. |

`TeeProxyConsensusStall` has up to ~30m worst-case detection latency: a 15m `increase(...)==0` window sits under a 15m `for`, and both are deliberately conservative to avoid false positives during naturally quiet, block-paced periods.
Operators who need faster paging should first shorten `for` — the `increase[15m]==0` clause already supplies most of the dwell time — and weigh that against the higher false-positive risk before also considering escalating a sustained full stall to critical.

Storage errors are already alerted and label-distinguished by `TeeProxyStorageErrors` via the `backend` (`redis`/`firestore`) and `namespace` (`results`/`backups`/`backupIndex`) labels (see the `storage` group table below).
Operators who want faster paging specifically on Firestore or on the backup path can add a higher-severity rule filtered to `backend="firestore"` or `namespace="backups"`; this is severity tuning on an existing signal, not new coverage, so no additional rule ships here.

## `http`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_http_requests_total` | counter | `server`, `route`, `status_class` | HTTP requests by server, route, and status class. |
| `teeproxy_http_request_duration_seconds` | histogram | `server`, `route` | HTTP request handling latency by server and route. |

Label values: `server` is `internal` or `external`; `route` is the matched mux route template (e.g. `POST /queue/{queueID}`) or `unmatched`; `status_class` is `1xx`/`2xx`/`3xx`/`4xx`/`5xx`.
A handler panic is recorded once with `status_class` `5xx` and a duration sample up to the point the panic reaches the middleware, then re-raised so net/http still aborts the connection and logs it.

## `storage`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_storage_operations_total` | counter | `backend`, `namespace`, `operation`, `outcome` | Storage operations by backend, namespace, operation, and outcome. |
| `teeproxy_storage_operation_duration_seconds` | histogram | `backend`, `namespace`, `operation` | Storage operation latency by backend, namespace, and operation. |

Label values: `backend` is `redis` or `firestore`; `namespace` is `results`/`backups`/`backupIndex`; `operation` is `set`/`set_with_ttl`/`get`/`remove`; `outcome` is `success`/`not_found`/`error`.

## `queue`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_action_enqueue_total` | counter | `queue`, `result` | Action enqueue attempts by queue and result. |
| `teeproxy_action_dequeue_total` | counter | `queue`, `result` | Action dequeue attempts by queue and result; `action_not_found` and `error` consumed a queue ID whose body could not be fetched (an orphaned/lost action). |
| `teeproxy_action_queue_depth` | gauge (scrape-time) | `queue` | Pending submission IDs per queue. |
| `teeproxy_action_queue_depth_read_failures_total` | counter | `queue` | Scrape-time queue-depth (LLEN) read failures; while nonzero the depth gauge reports 0 and the backpressure alert is masked. |

Label values: `result` for dequeue is `success`/`empty`/`error`/`action_not_found`; `result` for enqueue is `success`/`store_error`/`queue_error`/`invalid_queue`; `queue` is `main`/`direct`/`backup` (and `other` only on an invalid queue ID).
During Redis degradation the depth gauge reads 0 and `TeeProxyQueueBackpressure` is expected to be masked; `TeeProxyStorageErrors` is the authoritative degradation signal.

## `voting`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_instructions_received_total` | counter | — | Instructions submitted. |
| `teeproxy_instructions_rejected_total` | counter | `reason` | Instructions rejected, by reason. |
| `teeproxy_votings_started_total` | counter | — | Votings opened (a new proposal box created). |
| `teeproxy_voting_threshold_duration_seconds` | histogram | — | Seconds from voting start to reaching threshold. Its `_count` is the number of votings finalized. |
| `teeproxy_finalized_action_enqueue_failed_total` | counter | — | Finalized actions that failed to enqueue to the main queue. |
| `teeproxy_finalized_action_lost_total` | counter | `reason` | Finalized actions dropped before the main-queue enqueue, by reason. |

Label values: `reason` is one of `wrong_tee_id`/`invalid_op`/`invalid_signature`/`invalid_voter`/`voting_ended`/`duplicate_signature`/`event_in_future`/`other`.
A voting is finalized exactly when it reaches threshold, so "votings finalized" is `voting_threshold_duration_seconds_count` (the histogram's observation count).
The threshold-duration histogram is intentionally unlabeled: consensus latency is governed by a protocol uniform across op types, so an `op_command` label would only stratify it by traffic mix.
Every started voting eventually either finalizes or expires, so expired votings are `votings_started_total − voting_threshold_duration_seconds_count` (exactly once all in-flight votings have closed; instantaneously this also includes votings still open within the proposal-expiration window).

On `finalized_action_lost_total`, `reason` is one of `build_error`/`send_cancelled`.
`build_error` is a (practically unreachable) action-serialization failure.
`send_cancelled` fires only when a finalized action's send to the forwarding channel is aborted during shutdown.
Enqueue failures are a separate, sibling failure mode counted by `finalized_action_enqueue_failed_total`, so total finalized-action loss is the sum of the two counters.
Both `reason` series are pre-initialized to 0 at startup so the first, possibly one-shot drop satisfies the `increase(...) > 0` alert on `build_error`.

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
| `teeproxy_result_channel_dropped_total` | counter | `channel` | Result fan-out messages dropped because the target channel was full, by channel. |

Label values: `op_command` is a bounded operation-command name, else `other`; `status_class` is `failed`/`final`/`transient`; `reason` is `bad_signer`/`wrong_tee_id`/`bootstrap`.
`results_processed_total` is counted after the identity gates but before storage, so a re-delivery that the storage override-guard later rejects (a duplicate of an already-persisted result) still counts here — it measures processed deliveries, not distinct stored results.
A `wrong_tee_id` rejection means a result was signed by a key other than the bound TEE identity (a tamper / mis-route signal); `bad_signer` is a malformed signature; `bootstrap` is a non-TEE_INFO result arriving before the identity is set (an expected startup transient).
`channel` is `key_actions`, `backups`, or `backup_trigger` — the three result fan-out targets a finalized action's side effect is queued to.
A drop here means a finalized-consensus side effect (key materialization or backup trigger) did not run immediately; the periodic wallet sync in the wallets service later reconciles key and backup state from the tee-node, so a drop delays rather than permanently loses the effect.

## `wallet`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_wallet_sync_total` | counter | `result` | Wallet key/proof sync cycles by result. |
| `teeproxy_wallet_keys_cached` | gauge (scrape-time) | — | Key proofs cached in the wallet service's in-memory store. |

Label values: `result` is `success`/`enqueue_error`/`parse_error`/`skipped`.
Node-wait failures during sync are already counted in `teeproxy_node_response_wait_total{path="wallet_key_info"|"wallet_key_proof"}` and are not duplicated here.
`enqueue_error` covers action-build/marshal/enqueue failures before the node wait.
`parse_error` covers a node wait that returned OK but whose response failed to decode.
`skipped` fires when a sync trigger arrives while a previous sync is still in progress — a wedged `syncing` flag shows as a sustained run of `skipped`.

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
| `teeproxy_attestation_posture` | gauge | `setting` | Attestation verification posture: 1 if the named setting is active, else 0. |

Label values: `result` is `ok` or `error`; `reason` is a bounded reason (`ok`, `magic_pass` for an accepted magic_pass sentinel, `other`, or a mapped verification-failure reason such as `magic_pass_disabled`, `pubkey_mismatch`, `jwt_invalid`, …); `setting` is one of `enabled`, `magic_pass_allowed`, `audience_check`, `code_hash_check`, `platform_check`, `debug_status_check`, `max_token_age_check`, `sec_boot_check`.
An accepted magic_pass is recorded as `result="ok",reason="magic_pass"` so it is distinguishable from a genuine JWT pass and is NOT covered by `TeeProxyAttestationFailing` (`result="error"`).
The full error-reason set is enumerated in `pkg/attestation` `ErrorReasons`, which metrics pre-initialization uses so every `{result="error"}` series exists at 0 from startup.

## `policy`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_policy_active_reward_epoch` | gauge | — | Reward epoch of the active signing policy, which is also the newest one the proxy holds. |
| `teeproxy_signing_policy_oldest_reward_epoch` | gauge (scrape-time) | — | Oldest reward epoch with a signing policy still resident in the in-memory voting window. |
| `teeproxy_policy_last_fetch_timestamp_seconds` | gauge | — | Unix time of the last error-free signing-policy update-loop iteration. |
| `teeproxy_policy_update_total` | counter | `result` | Signing-policy update-loop iterations by outcome. |

Label values: `result` is `empty`/`fetch_error`/`reconciled`/`reconcile_error`/`build_failed`/`enqueue_failed`/`await_failed`/`rejected`/`applied`.
In steady state only `empty` increments, once per `signing_policy_fetch_interval` — the next epoch is not yet on chain, which is the healthy heartbeat.
A wedged or persistently-failing loop is detected as staleness of `policy_last_fetch_timestamp_seconds`, via `time() - teeproxy_policy_last_fetch_timestamp_seconds`, since no error-free iteration refreshes it.
The gauge is seeded at startup (in `Initialize`), so `time() - metric` is meaningful from boot and does not false-fire before the loop's first iteration.
`rejected` uniquely surfaces repeated node rejections of a rollover: `WaitOnResponse` returns `err == nil` for a `status != 1` rejection, so the node-wait metric records it as `result="ok"` and only this counter distinguishes it.

The proxy does not persist signing policies.
The active policy (`policy_active_reward_epoch`) is the newest one it has ingested, so the resident window runs from `signing_policy_oldest_reward_epoch` up to `policy_active_reward_epoch`.
The window is the voting cyclic buffer, whose size is `voting.history_size` (default 3).

## `liveness`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_ready` | gauge | — | 1 if the last readiness check passed, else 0. |
| `teeproxy_info_service_delay_seconds` | gauge (scrape-time) | — | Seconds since the last successful TEE info refresh. |

`info_service_delay_seconds` starts at zero at process start: `info.NewService` sets `LastUpdated` to the construction time, so the gauge only rises if refreshes genuinely stop, not as an artifact of not having refreshed yet.
`teeproxy_ready` reflects only the outcome of the last evaluation triggered by a `GET /ready` call — it is not recomputed on a `/metrics` scrape.
It requires a periodic external readiness probe (a Kubernetes-style `readinessProbe` hitting `/ready` every 10-30s is the intended deployment) to stay fresh well inside the 2m window `TeeProxyNotReady` uses.
If nothing ever calls `/ready`, the gauge stays at its zero-value default and `TeeProxyNotReady` pages permanently starting 2 minutes after boot; that is a deployment misconfiguration (no readiness probe wired), not a proxy bug.

## `node`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_node_response_wait_duration_seconds` | histogram | `path` | Synchronous wait for a TEE-node response, by path. |
| `teeproxy_node_response_wait_total` | counter | `path`, `result` | TEE-node response waits by path and outcome. |
| `teeproxy_machinepath_poll_total` | counter | `result` | Machine-path poll cycles by result. |

Label values: `path` is `info`/`machinepath`/`wallet_key_info`/`wallet_key_proof`/`policy_update`; `result` (node-wait) is `ok`/`timeout`/`cancelled`/`error`; `result` (machinepath poll) is `fetch_error`/`build_error`/`enqueue_error`/`no_change`/`confirmed`.
`policy_update` is the `UPDATE_POLICY` confirmation wait (2m timeout), fired roughly once per reward epoch during a signing-policy rollover.
This is the proxy's synchronous round-trip to the TEE node (the wait inside `WaitOnResponse`).
A rising `timeout` share, or a p99 approaching the per-path response timeout (2–3 minutes), is the leading signal that the node is slow or unreachable — and the `path` label localizes partial degradation (e.g. `wallet_key_proof` slow while `info` is fine).
`machinepath_poll_total` is scoped to the pre-delivery and outcome legs of the machine-path poll loop only — the node-wait leg for the same poll is `teeproxy_node_response_wait_total{path="machinepath"}`, and the post-wait status rejection is `teeproxy_results_processed_total{op_command="SET_MACHINE_PATH_LIST"}`.

## `runtime`

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `teeproxy_build_info` | gauge | `version`, `revision`, `go_version` | Constant 1, labeled with build metadata (version, VCS revision, and Go version). |
| `go_*` | various | — | Standard Go runtime collector (includes `go_goroutines`, GC stats, etc.). |
| `process_*` | various | — | Standard process collector (CPU, memory, file descriptors, etc.). |
