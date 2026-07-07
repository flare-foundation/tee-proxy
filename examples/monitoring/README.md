# Monitoring examples

Example monitoring artifacts for the TEE proxy's Prometheus metrics.
See [`../../METRICS.md`](../../METRICS.md) for the full metric catalog and an `Alerting` overview.

## `alerts.yaml`

Example Prometheus alerting rules in the native `rule_files` format.
They are **starting points**, not a turnkey policy: thresholds mirror the proxy's in-code constants (noted in each rule) but the right values depend on your traffic and SLOs.

A few things to adjust before use:

- **Scrape selector.** The expressions carry no `job`/instance selector. Add yours (e.g. `{job="tee-proxy"}`) so the rules scope to the proxy.
- **Metric groups.** Each rule needs its metric group enabled in the proxy's `[metrics]` config; the rule notes which one. A disabled group means the series never exist and the rule never fires.
- **`TeeProxyDown`** is example-only: the `up{job="tee-proxy"}` selector is your scrape config.

### Validate

```bash
promtool check rules examples/monitoring/alerts.yaml
```

CI runs this check on every push (the `alerts` job), so the rules cannot silently rot.

### Use under the Prometheus Operator

`promtool` validates the native format above.
To deploy as a `PrometheusRule` custom resource, copy the `groups:` block into the resource's `.spec`.

## Dashboards

No Grafana dashboard is shipped here yet.
A dashboard JSON drifts from the metric set with no CI verification, so it is intentionally left out for now; the metric names in `METRICS.md` and the alert expressions above are enough to build one.
