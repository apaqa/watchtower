# WatchTower

A lightweight, self-contained monitoring platform written in Go. A single binary collects system metrics, stores time-series data, evaluates alert rules, tails logs, probes HTTP endpoints, records distributed traces, and manages a registry of connected agents — all without external databases or message queues.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         WatchTower Process                           │
│                                                                      │
│  ┌───────────┐  POST /api/v1/metrics       ┌─────────────────────┐  │
│  │  System   │ ──────────────────────────▶ │  Ingest API  :9090  │  │
│  │  Agent    │  POST /api/v1/agents/       │  WQL Query API       │  │
│  │(gopsutil) │       register (heartbeat)  │  Alert API           │  │
│  └───────────┘                             │  Log API             │  │
│                                            │  Probe API           │  │
│  ┌───────────┐  POST /api/v1/logs          │  Trace API           │  │
│  │  Log      │ ──────────────────────────▶ │  Agent Registry API  │  │
│  │  Agent    │                             └──────────┬──────────┘  │
│  └───────────┘                                        │ Write/Query  │
│                                                       ▼              │
│  ┌───────────┐  probe_status metric         ┌──────────────────┐    │
│  │  Endpoint │ ──────────────────────────▶  │  In-Memory TSDB  │    │
│  │  Probes   │                              │ + Gorilla Disk    │    │
│  └───────────┘                              │   Persistence     │    │
│                                             └────────┬─────────┘    │
│  ┌───────────┐  POST /api/v1/traces                  │              │
│  │  Trace    │ ──────────────────────────▶  ┌────────────────┐      │
│  │  Agent    │                              │  Trace Store   │      │
│  └───────────┘                              │(1000 / 1h TTL) │      │
│                                             └────────┬───────┘      │
│                                                      │ REST          │
│                                                      ▼               │
│                                      ┌───────────────────────────┐  │
│  Browser ◀─── WebSocket ──────────   │   Dashboard  :8080        │  │
│           ◀─── HTML / JS ──────────  │   Metrics · Logs · Alerts │  │
│           ◀─── Panel API ──────────  │   Endpoints · Traces      │  │
│                                      │   Agents · Custom Panels  │  │
│                                      └───────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

## Features

### System Agent
- Collects CPU, memory, and disk usage every 5 seconds via `gopsutil`
- Pushes metrics to the local ingest API as JSON

### Time-Series Database
- In-memory storage with 1-hour rolling window
- **Gorilla-encoded** disk persistence (Delta-of-Delta timestamps + XOR float values)
- Chunks written to `watchtower-data/chunks/*.wtkc`, auto-restored on startup
- Background flush every 30 seconds; final flush on shutdown

### WQL Query Language
- PromQL-inspired query language for the TSDB
- Aggregation functions: `avg`, `max`, `min`, `sum`, `count`, `rate`, `last`
- Label filtering: `avg(cpu_usage_percent{host="node1"}[5m])`
- Time ranges: `[1m]`, `[5m]`, `[15m]`, `[1h]`, `[1d]`
- Arithmetic: `avg(memory_used_bytes[5m]) / 1073741824`
- Comparison (returns bool): `avg(cpu_usage_percent[5m]) > 80`
- Group-by: `sum(cpu_usage_percent[5m]) by (host)`

### Alert Engine
- Configurable rules evaluated against WQL expressions
- State machine: `inactive → pending → firing → resolved`
- Optional `duration` to require sustained condition before firing
- Webhook notifications with JSON payload on state transitions

### Log Collection
- In-memory ring buffer: 10 000 entries per source, 24-hour retention
- Full-text search and `/regex/` pattern matching
- Batch and single-entry ingestion APIs
- Log agent generates synthetic system events every 10 seconds

### HTTP Endpoint Monitoring
- Per-probe goroutines poll any HTTP(S) endpoint on a configurable interval
- Records response time and up/down status; writes `probe_status` and `probe_response_ms` to TSDB
- Stores last 100 results per probe; calculates uptime percentage
- Runtime add/remove via REST API

### Prometheus Compatibility
- **Ingest**: `POST /api/v1/metrics/prometheus` accepts the Prometheus text exposition format; all metric types (counter, gauge, histogram, summary) are stored as regular time-series
- **Scrape**: `GET /metrics` exports all TSDB metrics in Prometheus text format so WatchTower itself can be scraped by any Prometheus instance
- Comment lines (`# HELP`, `# TYPE`) are silently skipped; invalid lines are skipped without aborting the batch
- Labels, escaped label values, timestamps (optional, milliseconds), and special values (`+Inf`, `-Inf`, `NaN`) are all supported

### API Key Authentication
- **Open mode**: if no keys are configured, all `/api/` endpoints are publicly accessible
- **Authenticated mode**: once any key is added (via config or API), every `/api/` request must include `X-API-Key: <key>` header (or `?api_key=<key>` query param)
- Permissions: `read` · `write` · `admin` (admin implies read + write)
- `/metrics` scrape endpoint and the dashboard static files are always public
- Keys can be pre-loaded from `watchtower.yaml` under `api_keys:` or created dynamically via the key management API
- Generated keys are 64-character random hex strings; only shown once at creation time
- The dashboard header includes an API Key input that stores the key in `localStorage` and injects it into all API requests

### Distributed Tracing
- In-memory trace store: up to 1 000 traces, 1-hour retention, ring-buffer eviction
- Thread-safe with separate index by `trace_id` and by `service_name`
- Ingestion accepts any batch of spans; spans with the same `trace_id` are merged into one trace
- `Trace` computed metadata: `start_time`, `duration_ms`, `service_names`, `has_error`
- Trace agent generates synthetic 4-span traces every 10 seconds simulating a real API call chain
- 20% chance of error spans for realistic error-rate simulation

### Agent Registry
- Agents register via `POST /api/v1/agents/register` on startup and send heartbeats every 15 seconds
- Thread-safe in-memory registry indexed by `agent_id` (UUID v4, generated at startup)
- Background health-check goroutine marks agents offline after 30 seconds of silence
- Each agent reports: `hostname`, `ip_address` (auto-detected), `os`, `arch`, `labels`
- Full CRUD REST API: list, get, delete agents
- System agent now embeds `agent_id` label on every metric data point

### Service Map
- Analyzes all in-memory traces to build a live service dependency graph
- **Nodes**: each unique `service_name` seen across spans, typed automatically by keyword matching (api/database/cache/queue/external)
- **Edges**: directed, from parent span's service to child span's service; only cross-service calls appear as edges
- Per-node metrics: `request_count`, `error_count`, `avg_latency_ms`
- Per-edge metrics: `request_count`, `error_rate` (0–1), `avg_latency_ms`
- `GET /api/v1/servicemap` returns `{ nodes: [...], edges: [...] }` as JSON
- Dashboard **Service Map** tab: SVG visualization with circular layout, color-coded by type, red border on high-error nodes (>5%), auto-refreshes every 10 seconds

### SLO / SLI Tracking
- Define SLOs with a name, target percentage (0–100 exclusive), WQL query, metric type, and window (1d/7d/30d)
- **SLI**: result of the WQL query clamped to 0–100; scalar, bool (`true=100`, `false=0`), or vector average
- **Error budget**: `(1 − target%) × window_minutes` total; `(1 − sli%) × window_minutes` consumed
- `GET /api/v1/slos` returns all SLOs with current SLI, budget total, budget consumed, budget remaining %
- `POST /api/v1/slos` creates an SLO; `DELETE /api/v1/slos/{name}` removes it
- Dashboard **SLO Status** section in Metrics tab: progress bar per SLO (green/yellow/red), "+ Add SLO" modal

### Anomaly Detection
- Background goroutine scans all active TSDB metric series every 30 seconds
- **Z-score detection**: uses the last 30-minute rolling window as a baseline; flags the latest point as an anomaly if `|value − mean| / stddev ≥ 3`
- Skips series with fewer than 5 samples or near-zero standard deviation (flat-line guard)
- Severity classification: Z ≥ 5 → **high**, Z ≥ 3.5 → **medium**, Z ≥ 3 → **low**
- Ring buffer retains the last 500 `AnomalyEvent` records
- `GET /api/v1/anomalies` returns all recent events; `?metric=<name>` filters by metric name
- Dashboard **Anomalies** section in Alerts tab: color-coded by severity (red/yellow/blue), shows actual vs. expected value and Z-score, live filter bar

### Metric Correlation
- Computes **Pearson correlation coefficient** between any two TSDB metrics over a configurable time window
- Time windows: `5m`, `15m`, `30m`, `1h`, `6h`, `1d`
- Aligns series by 1-minute buckets; only overlapping timestamps contribute to the coefficient
- Interpretation labels: `strong_positive` (|r| ≥ 0.7) · `weak_positive` · `none` · `weak_negative` · `strong_negative`
- Returns scatter-plot data points (`{x, y}` pairs) alongside the coefficient for direct Chart.js rendering
- **Auto-correlation**: `GET /api/v1/correlate/auto?metric=<name>` scans all other metrics and returns the top 5 most correlated (by |r|)
- Dashboard **Correlations** widget in Metrics tab: two metric dropdowns, window selector, scatter chart, "Auto-correlate" button

### Downsampling
- Automatic background aggregation of high-resolution raw data into lower-resolution summaries (runs every 5 minutes)
- Three tiers: raw (5s interval, 1h retention) → `metric:1m` (1-minute averages, 24h retention) → `metric:5m` (5-minute averages, 7-day retention)
- Downsampled series stored with the same labels as the source, using name suffixes `:1m` / `:5m`
- Bucket timestamps are always aligned to minute / 5-minute boundaries; only completed buckets are written (no partial-bucket bias)
- Existing downsampled points are never overwritten — only new completed buckets are appended

### Retention Policies
- Three built-in policies: `raw` (≤1h), `1m-ds` (≤24h), `5m-ds` (≤7d) — aligned with the downsampling tiers
- Custom policies configurable in `watchtower.yaml` under `retention_policies:` — each with `name`, `match_pattern` (regex), `max_age_seconds`, `max_points_per_series`
- Custom policies take precedence over built-in policies when both match a series name
- Background goroutine enforces all policies every 10 minutes; also callable on-demand via Admin GC API
- `GET /api/v1/retention` — list all policies (built-in + custom)
- `POST /api/v1/retention` — create a custom policy
- `DELETE /api/v1/retention/{name}` — delete a custom policy (built-in policies are protected)

### Admin API
- `GET /api/v1/admin/status` — full system status: uptime, version, Go version, goroutine count, heap memory, TSDB series count, total data points
- `POST /api/v1/admin/gc` — force TSDB garbage collection + Go runtime GC
- `POST /api/v1/admin/snapshot` — flush TSDB data to disk immediately (no-op when storage is disabled)
- `GET /api/v1/admin/config` — running config with API key values redacted (`***REDACTED***`)
- `POST /api/v1/admin/reload` — re-parse `watchtower.yaml` and hot-update server/agent/retention fields (alert rules and endpoints require restart)
- Dashboard **Admin** tab: system info cards, Force GC / Snapshot / Reload Config buttons, retention policy table with inline add/delete — auto-refreshes every 30 seconds

### Resource Forecasting & Capacity Planning
- **Linear regression** (`y = mx + b`) fitted over the most recent 30 minutes of TSDB data
- **ForecastResult**: metric name, current value, predicted 1h / 24h / 7d, trend direction, confidence score (R²)
- **Trend classification**: `increasing` / `decreasing` / `stable` based on per-hour relative change rate
- **Exhaustion estimate**: for bounded metrics (e.g. `disk_usage_percent`), predicts the Unix timestamp when they will reach a configurable threshold
- **Capacity report**: one-call JSON summary for CPU, Memory, and Disk — current avg, peak, trend, 24h prediction, days-until-full, and health status (`healthy` / `warning` / `critical`)
- Health thresholds: CPU/Memory critical ≥ 90% predicted 24h; Disk critical ≤ 7 days until full, warning ≤ 30 days
- `GET /api/v1/forecast?metric=<name>` — returns ForecastResult (1h/24h/7d predictions + trend + R²)
- `GET /api/v1/forecast?metric=<name>&threshold=<value>` — returns ExhaustionEstimate (hours/days until threshold)
- `GET /api/v1/capacity` — returns full CapacityReport for all system metrics
- Dashboard **Forecasting & Capacity** section in Metrics tab: capacity summary cards (CPU/Memory/Disk) with trend arrow (↑↓→) and health badge; interactive forecast widget with dashed prediction line chart

### Process Monitoring
- Collects the top 20 processes by CPU usage every 10 seconds via `gopsutil/v3/process`
- Per-process data: PID, name, CPU%, memory (MB + %), status (running/sleeping/zombie), start time, command line, username
- Writes `process_cpu{name=…,pid=…}` and `process_memory_mb{name=…,pid=…}` to TSDB for alerting and WQL queries
- `GET /api/v1/processes` returns current snapshot sorted by CPU; `?sort=memory` sorts by memory instead

### Status Page
- Public-facing status page at `GET /status` — no API key required
- Shows overall system status: **Operational** (all probes up) · **Degraded** (some down) · **Down** (all down)
- Lists all monitored endpoints with current status, uptime %, and response time
- Lists all SLOs with current SLI vs. target
- Shows last 10 alert firing/resolved events as an incident history
- Clean, minimal dark-theme HTML design (separate from the main dashboard)
- `GET /status/badge` returns an SVG badge for embedding in READMEs or external dashboards

### Metrics Aggregation Pipeline
- Define rules to continuously aggregate raw metrics into new derived series
- Each rule specifies: input metric, output metric, aggregation function, time window, and optional `group_by` label keys
- **Aggregation functions**: `avg`, `sum`, `max`, `min`, `count`, `p50`, `p95`, `p99`
- **Windows**: `1m`, `5m`, `1h`
- Background goroutine evaluates all rules every minute and writes results back to TSDB, making them queryable via WQL
- `group_by` partitions the input series by label values; each partition produces a separate output series
- Percentile functions use linear interpolation for accuracy

### Data Export
- Export TSDB metrics, logs, or traces as **CSV** or **JSON** via GET requests
- **Metrics export**: `GET /api/v1/export/metrics?format=csv&name=<metric>&start=<ms>&end=<ms>`
- **Logs export**: `GET /api/v1/export/logs?format=csv&level=<level>&source=<src>&start=<ms>&end=<ms>`
- **Traces export**: `GET /api/v1/export/traces?format=json&start=<ms>&end=<ms>` (CSV flattens to span-level rows)
- Time range defaults to the past hour; all params optional
- Response includes `Content-Disposition: attachment` header for direct browser download
- Dashboard **Export** buttons on Metrics and Logs tabs trigger instant download

### Multi-Channel Notifications
- Five channel types: **Console** (stdout), **Webhook** (generic JSON POST), **Slack** (attachments with color-coded severity), **Discord** (embeds), **Email** (net/smtp with PLAIN auth)
- `notify.Router` routes each `Notification` to all channels whose severity filter matches; dispatches in goroutines so notifications never block the alert evaluation loop
- Per-channel severity filter: e.g. send Slack only for `critical`, email for all
- Notification history ring buffer stores the last 200 results (channel type, alert name, severity, sent/failed status, error message)
- `GET /api/v1/notifications` returns the full history as JSON
- Alert engine checks: if a `Router` is set → call `router.Dispatch()`; else fall back to rule-level `webhook_url`
- Channels configured via `notifications.channels[]` in `watchtower.yaml`

### Custom Dashboard Panels
- Users can define custom metric panels from any WQL expression via the dashboard UI
- In-memory `PanelStore` backed by ordered insertion; supports `stat` and `gauge` panel types
- Per-panel configurable auto-refresh interval (minimum 5 seconds, default 30 seconds)
- REST API at `:8080/api/v1/dashboard/panels`: `POST` to create, `GET` to list, `DELETE /{id}` to remove
- Panels persist for the lifetime of the process and appear in the Metrics tab "Custom Panels" section

### Dashboard
- Dark-theme single-page app served at `:8080`
- Live Chart.js line charts via WebSocket push (5-second interval)
- **Eight tabs**: **Metrics** · **Logs** · **Alerts** · **Endpoints** · **Traces** · **Agents** · **Service Map** · **Processes**
- **Aggregation Rules** section in Metrics tab: list active rules, "+ Add Rule" modal, delete rules
- **Export buttons** on Metrics and Logs tabs: one-click CSV/JSON download for the current dataset
- **Processes tab**: live process table with CPU/Memory columns; clickable column headers for client-side sorting; search filter bar; red highlight for high-CPU (>50%) or high-memory (>1GB) processes; auto-refreshes every 10 seconds
- **Anomalies section** in Alerts tab: color-coded events (red=high, yellow=medium, blue=low), actual vs expected value, Z-score, live metric filter
- **Correlations widget** in Metrics tab: two metric selects + window selector + "Analyze" button renders a scatter chart with the Pearson r; "Auto-correlate" lists the top 5 related metrics
- **Forecasting & Capacity** section in Metrics tab: capacity summary cards (CPU/Memory/Disk) with trend arrow (↑↓→) and health badge (healthy/warning/critical); interactive forecast widget — select metric, optionally enter a threshold, click "Forecast" to see 1h/24h/7d predictions and a dashed-line projection chart
- WQL query box with instant results
- Log viewer with full-text/regex search and level filter
- Alert manager with rule creation modal and state history
- Endpoint cards with status badge, response time, uptime %, sparkline chart, and 20-check status dots
- Trace list with service filter and min-duration filter; click any trace to expand a proportional **waterfall diagram**
- **Agents tab**: table with hostname, IP, OS, status badge (online/offline), last-seen (relative), registered time; "X Online / Y Offline" summary; auto-refreshes every 15 seconds
- **Custom Panels** section in the Metrics tab: "+ Add Panel" button opens a modal; each panel auto-refreshes its WQL query at the configured interval
- **Notification History** section in the Alerts tab: lists the last 50 sent/failed notifications with channel type, severity badge, and error details
- **Theme toggle**: 🌙/☀️ button in header switches between dark and light themes; preference stored in `localStorage`
- **Auto-refresh selector** in header: 5s / 10s / 30s / 60s / Paused; preference stored in `localStorage`; "Paused" suspends metric chart and UI updates while keeping WebSocket connected

### YAML Configuration
- `watchtower.yaml` at project root controls all ports, intervals, and pre-loaded rules/probes
- Missing file → safe defaults used silently

## Quick Start

**Requires Go 1.21+**

```bash
git clone https://github.com/apaqa/watchtower.git
cd watchtower

go run ./cmd/watchtower
```

Then open **http://localhost:8080** in your browser.

To build a binary:

```bash
go build -o watchtower ./cmd/watchtower
./watchtower
```

### Endpoints

| Service | Address |
|---------|---------|
| Dashboard | http://localhost:8080 |
| Ingest API | http://localhost:9090/api/v1/metrics |
| WQL Query | http://localhost:9090/api/v1/query |
| Alert API | http://localhost:9090/api/v1/alerts |
| Log API | http://localhost:9090/api/v1/logs |
| Probe API | http://localhost:9090/api/v1/probes |
| Trace API | http://localhost:9090/api/v1/traces |
| Agent Registry | http://localhost:9090/api/v1/agents |
| Auth / Key Mgmt | http://localhost:9090/api/v1/auth/keys |
| Prometheus Ingest | http://localhost:9090/api/v1/metrics/prometheus |
| Prometheus Scrape | http://localhost:9090/metrics |
| Custom Panels API | http://localhost:8080/api/v1/dashboard/panels |
| Pipeline API | http://localhost:9090/api/v1/pipeline/rules |
| Export API | http://localhost:9090/api/v1/export/metrics |
| Process API | http://localhost:9090/api/v1/processes |
| Status Page | http://localhost:9090/status |
| Status Badge | http://localhost:9090/status/badge |
| Anomaly API | http://localhost:9090/api/v1/anomalies |
| Correlation API | http://localhost:9090/api/v1/correlate |

## Configuration

Place `watchtower.yaml` in the project root. All fields are optional — defaults are used for any missing key.

```yaml
server:
  ingest_port: 9090       # ingest + probe + alert + log API port
  dashboard_port: 8080    # web UI port

agent:
  enabled: true           # enable the system metrics agent
  interval_seconds: 5     # collection interval

retention:
  metrics_hours: 1        # TSDB in-memory retention window
  logs_hours: 24          # log ring-buffer retention window

# HTTP endpoints to probe periodically
endpoints:
  - name: my-api
    url: https://api.example.com/health
    method: GET
    expected_status: 200
    interval_seconds: 30
    timeout_ms: 5000
    headers:
      Authorization: "Bearer token"

# Alert rules loaded at startup (equivalent to POST /api/v1/alerts/rules)
alerts:
  - name: high_cpu
    wql_expression: avg(cpu_usage_percent[5m])
    operator: ">"
    threshold: 85
    duration: 5m
    severity: warning
  - name: high_memory
    wql_expression: avg(memory_usage_percent[5m])
    operator: ">"
    threshold: 90
    duration: 5m
    severity: critical
```

## WQL Query Language

```
# Aggregation + time window
avg(cpu_usage_percent[5m])
max(memory_usage_percent[1h])
rate(cpu_usage_percent[5m])      # per-second rate of change
last(disk_usage_percent[5m])     # most recent value

# Label filters
avg(cpu_usage_percent{host="node1"}[5m])
max(cpu_usage_percent{host="server1",env="prod"}[1h])

# Arithmetic
avg(memory_used_bytes[5m]) / 1073741824    # bytes → GB

# Comparison (returns bool)
avg(cpu_usage_percent[5m]) > 80

# Group-by
sum(cpu_usage_percent[5m]) by (host)
avg(memory_usage_percent[5m]) by (host)
```

Query via HTTP:

```bash
# Scalar
curl "http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])"
# {"type":"scalar","scalar":45.23}

# Vector
curl "http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])%20by%20(host)"
# {"type":"vector","vector":[{"labels":{"host":"DESKTOP"},"value":45.23}]}

# Bool
curl "http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])%20>%2080"
# {"type":"bool","bool":false}
```

## API Reference

### Metrics

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/metrics` | Ingest metric data points (JSON array) |
| `GET`  | `/api/v1/query?q=<WQL>` | Execute a WQL query |

```bash
curl -X POST http://localhost:9090/api/v1/metrics \
  -H "Content-Type: application/json" \
  -d '[{"name":"my_metric","labels":{"host":"node1"},"value":42.5}]'
```

### Alerts

| Method | Path | Description |
|--------|------|-------------|
| `POST`   | `/api/v1/alerts/rules` | Create an alert rule |
| `GET`    | `/api/v1/alerts/rules` | List all rules with current state |
| `DELETE` | `/api/v1/alerts/rules/{name}` | Delete a rule |
| `GET`    | `/api/v1/alerts/active` | List currently firing alerts |
| `GET`    | `/api/v1/alerts/history` | Last 100 state-change events |

```bash
curl -X POST http://localhost:9090/api/v1/alerts/rules \
  -H "Content-Type: application/json" \
  -d '{
    "name": "high_cpu",
    "wql_expression": "avg(cpu_usage_percent[5m])",
    "operator": ">",
    "threshold": 80,
    "duration": "5m",
    "severity": "critical",
    "webhook_url": "https://hooks.example.com/notify"
  }'
```

Alert state machine:

```
inactive ──(condition true, duration=0)──▶ firing ──(condition false)──▶ resolved ──▶ inactive
inactive ──(condition true, duration>0)──▶ pending ──(sustained)──▶ firing
                                          └──(condition false)──▶ inactive
```

Webhook payload:

```json
{
  "alert_name": "high_cpu",
  "severity": "critical",
  "value": 95.23,
  "message": "avg(cpu_usage_percent[5m]) = 95.2300 (threshold: > 80.0000)",
  "state": "firing",
  "fired_at": "2026-03-22T10:00:00Z"
}
```

### Logs

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/logs` | Ingest log entries (JSON array) |
| `POST` | `/api/v1/logs/single` | Ingest a single log entry |
| `GET`  | `/api/v1/logs` | Search logs (`q`, `level`, `source`, `limit`) |

```bash
# Batch ingest
curl -X POST http://localhost:9090/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '[{"level":"error","source":"myapp","message":"db connection failed"}]'

# Search (supports /regex/ patterns)
curl "http://localhost:9090/api/v1/logs?q=error&level=error&limit=50"
curl "http://localhost:9090/api/v1/logs?q=%2Fcode+%5Cd%2B%2F"   # /code \d+/
```

Log entry schema:

```json
{
  "timestamp": 1711000000000,
  "level": "error",
  "source": "myapp",
  "message": "database connection failed",
  "labels": {"host": "node1", "env": "prod"}
}
```

`timestamp` is Unix milliseconds and may be omitted (server fills it in). `level` is one of `debug`, `info`, `warn`, `error`.

### Endpoint Probes

| Method | Path | Description |
|--------|------|-------------|
| `GET`    | `/api/v1/probes` | List all probes with current status |
| `POST`   | `/api/v1/probes` | Add a probe |
| `DELETE` | `/api/v1/probes/{name}` | Remove a probe |
| `GET`    | `/api/v1/probes/{name}/history` | Last 100 check results |

```bash
# Add a probe
curl -X POST http://localhost:9090/api/v1/probes \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-api",
    "url": "https://api.example.com/health",
    "method": "GET",
    "expected_status": 200,
    "interval_seconds": 30,
    "timeout_ms": 5000
  }'

# View status
curl http://localhost:9090/api/v1/probes

# Remove
curl -X DELETE http://localhost:9090/api/v1/probes/my-api
```

Probe status response:

```json
{
  "name": "my-api",
  "url": "https://api.example.com/health",
  "status": "up",
  "response_ms": 142,
  "status_code": 200,
  "uptime_pct": 99.5,
  "last_check": 1711000000000,
  "recent_history": [
    {"timestamp": 1711000000000, "status": "up", "status_code": 200, "response_ms": 142}
  ]
}
```

### Traces

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/traces` | Ingest a batch of spans (JSON array); spans sharing `trace_id` are merged |
| `GET`  | `/api/v1/traces` | List trace summaries (`service`, `limit`, `min_duration` filters) |
| `GET`  | `/api/v1/traces/{trace_id}` | Get the full trace with all spans |

```bash
# Ingest spans
curl -X POST http://localhost:9090/api/v1/traces \
  -H "Content-Type: application/json" \
  -d '[
    {"trace_id":"abc123","span_id":"root","operation_name":"HTTP GET /api","service_name":"gateway",
     "start_time":1711000000000,"duration_ms":120,"status":"ok"},
    {"trace_id":"abc123","span_id":"db01","parent_span_id":"root","operation_name":"db.query",
     "service_name":"user-service","start_time":1711000000010,"duration_ms":80,"status":"ok"}
  ]'

# List recent traces (filter to a specific service, min 50ms)
curl "http://localhost:9090/api/v1/traces?service=gateway&min_duration=50&limit=20"

# Get full trace
curl "http://localhost:9090/api/v1/traces/abc123"
```

Span schema:

```json
{
  "trace_id":       "abc123",
  "span_id":        "root",
  "parent_span_id": "",
  "operation_name": "HTTP GET /api/users",
  "service_name":   "api-gateway",
  "start_time":     1711000000000,
  "duration_ms":    120,
  "status":         "ok",
  "tags":           {"http.method": "GET"},
  "logs":           [{"timestamp": 1711000000050, "message": "cache miss"}]
}
```

`status` is `"ok"` or `"error"`. `parent_span_id` is omitted (or empty string) for root spans.

### Prometheus Compatibility

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/metrics/prometheus` | Ingest metrics in Prometheus text format |
| `GET`  | `/metrics` | Scrape endpoint — exports all TSDB metrics in Prometheus format |

```bash
# Push from a Prometheus exporter / your own app
curl -X POST http://localhost:9090/api/v1/metrics/prometheus \
  -H "Content-Type: text/plain" \
  --data-binary '
# HELP my_app_requests_total Total requests
# TYPE my_app_requests_total counter
my_app_requests_total{method="GET",status="200"} 1027 1395066363000
my_app_requests_total{method="POST",status="500"} 3 1395066363000
my_app_latency_seconds{quantile="0.99"} 0.034
'

# Prometheus scrape config to pull from WatchTower
# scrape_configs:
#   - job_name: watchtower
#     static_configs:
#       - targets: ['localhost:9090']
```

### API Key Authentication

| Method | Path | Description |
|--------|------|-------------|
| `POST`   | `/api/v1/auth/keys` | Create a new API key (admin required when keys exist) |
| `GET`    | `/api/v1/auth/keys` | List all keys (masked — last 4 chars only) |
| `DELETE` | `/api/v1/auth/keys/{name}` | Revoke a key by name |

```bash
# Create an admin key (open mode — no existing keys)
curl -X POST http://localhost:9090/api/v1/auth/keys \
  -H "Content-Type: application/json" \
  -d '{"name": "my-admin", "permissions": ["admin"]}'
# Returns: {"name":"my-admin","key":"<64-char-hex>","permissions":["admin"],...}
# Save the key — it is only shown once.

# Use the key for subsequent requests
curl http://localhost:9090/api/v1/agents \
  -H "X-API-Key: <64-char-hex>"

# Create a read-only key (requires admin key)
curl -X POST http://localhost:9090/api/v1/auth/keys \
  -H "X-API-Key: <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{"name": "readonly", "permissions": ["read"]}'

# Revoke a key
curl -X DELETE http://localhost:9090/api/v1/auth/keys/readonly \
  -H "X-API-Key: <admin-key>"
```

Pre-load keys in `watchtower.yaml`:

```yaml
api_keys:
  - name: admin-key
    key: "your-64-char-random-hex-string"
    permissions: [admin]
  - name: ci-readonly
    key: "another-random-key"
    permissions: [read]
```

### Agent Registry

| Method | Path | Description |
|--------|------|-------------|
| `POST`   | `/api/v1/agents/register` | Register or heartbeat (upsert by `agent_id`) |
| `GET`    | `/api/v1/agents` | List all agents with current status |
| `GET`    | `/api/v1/agents/{id}` | Get a single agent |
| `DELETE` | `/api/v1/agents/{id}` | Remove an agent |

```bash
# Register (also used for heartbeats — same endpoint)
curl -X POST http://localhost:9090/api/v1/agents/register \
  -H "Content-Type: application/json" \
  -d '{
    "agent_id": "550e8400-e29b-41d4-a716-446655440000",
    "hostname": "node1",
    "ip_address": "192.168.1.10",
    "os": "linux",
    "arch": "amd64",
    "labels": {"env": "prod"}
  }'

# List
curl http://localhost:9090/api/v1/agents
```

Agent status response:

```json
{
  "agent_id":     "550e8400-e29b-41d4-a716-446655440000",
  "hostname":     "node1",
  "ip_address":   "192.168.1.10",
  "os":           "linux",
  "arch":         "amd64",
  "status":       "online",
  "registered_at": 1711000000000,
  "last_seen_at":  1711000060000,
  "labels":        {"env": "prod"}
}
```

An agent is marked `"offline"` if it has not sent a heartbeat within 30 seconds.

### Custom Panels

| Method | Path | Description |
|--------|------|-------------|
| `POST`   | `/api/v1/dashboard/panels` | Create a custom panel |
| `GET`    | `/api/v1/dashboard/panels` | List all panels |
| `GET`    | `/api/v1/dashboard/panels/{id}` | Get a panel |
| `DELETE` | `/api/v1/dashboard/panels/{id}` | Remove a panel |

```bash
# Create a panel
curl -X POST http://localhost:8080/api/v1/dashboard/panels \
  -H "Content-Type: application/json" \
  -d '{
    "id": "avg_cpu",
    "title": "Average CPU",
    "wql_query": "avg(cpu_usage_percent[5m])",
    "panel_type": "stat",
    "refresh_interval": 10
  }'

# List
curl http://localhost:8080/api/v1/dashboard/panels

# Remove
curl -X DELETE http://localhost:8080/api/v1/dashboard/panels/avg_cpu
```

Panel schema:

```json
{
  "id":               "avg_cpu",
  "title":            "Average CPU",
  "panel_type":       "stat",
  "wql_query":        "avg(cpu_usage_percent[5m])",
  "width":            1,
  "refresh_interval": 10,
  "created_at":       1711000000000
}
```

`panel_type` is `"stat"` (large number) or `"gauge"` (percentage). `refresh_interval` minimum is 5 seconds.

### Notification History

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/notifications` | List last 200 sent/failed notifications |

```bash
curl http://localhost:9090/api/v1/notifications
```

Response (array, newest first):

```json
[
  {
    "timestamp":    "2026-03-23T10:00:00Z",
    "channel_type": "slack",
    "alert_name":   "high_cpu",
    "severity":     "critical",
    "state":        "firing",
    "status":       "sent"
  },
  {
    "timestamp":    "2026-03-23T09:55:00Z",
    "channel_type": "webhook",
    "alert_name":   "high_memory",
    "severity":     "warning",
    "state":        "firing",
    "status":       "failed",
    "error":        "webhook POST failed: connection refused"
  }
]
```

Configure channels in `watchtower.yaml`:

```yaml
notifications:
  channels:
    - type: console                          # always logs to stdout
    - type: webhook
      url: https://hooks.example.com/alert
      severities: [critical, warning]       # omit to receive all
    - type: slack
      url: https://hooks.slack.com/services/T00/B00/xxx
      severities: [critical]
    - type: discord
      url: https://discord.com/api/webhooks/xxx/yyy
    - type: email
      smtp_host: smtp.example.com
      smtp_port: 587
      from: alerts@example.com
      to: [oncall@example.com]
      smtp_username: alerts@example.com
      smtp_password: secret
```

### Service Map

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/servicemap` | Build and return service dependency graph from all traces |

```bash
curl http://localhost:9090/api/v1/servicemap
```

Response:
```json
{
  "nodes": [
    { "name": "api-gateway", "type": "api", "request_count": 42, "error_count": 0, "avg_latency_ms": 24.5 },
    { "name": "user-postgres", "type": "database", "request_count": 38, "error_count": 1, "avg_latency_ms": 8.2 }
  ],
  "edges": [
    { "from_service": "api-gateway", "to_service": "user-postgres", "request_count": 38, "error_rate": 0.026, "avg_latency_ms": 8.2 }
  ]
}
```

Service type is inferred from the name: keywords like `postgres`/`mysql`/`mongo` → `database`, `redis`/`cache` → `cache`, `kafka`/`queue` → `queue`, `external` → `external`, anything else → `api`.

### SLO / SLI Tracking

| Method | Path | Description |
|--------|------|-------------|
| `GET`    | `/api/v1/slos` | List all SLOs with current SLI and error budget |
| `POST`   | `/api/v1/slos` | Create an SLO |
| `DELETE` | `/api/v1/slos/{name}` | Remove an SLO |

```bash
# Create an availability SLO
curl -X POST http://localhost:9090/api/v1/slos \
  -H "Content-Type: application/json" \
  -d '{
    "name":           "api-availability",
    "metric_type":    "availability",
    "target_percent": 99.9,
    "wql_query":      "avg(availability_percent[7d])",
    "window":         "7d"
  }'

# List all SLOs with computed status
curl http://localhost:9090/api/v1/slos
```

Response entry:
```json
{
  "slo": { "name": "api-availability", "target_percent": 99.9, "window": "7d", ... },
  "current_sli": 99.95,
  "error_budget_total": 10.08,
  "error_budget_consumed": 3.02,
  "error_budget_pct": 70.0,
  "healthy": true
}
```

The WQL query must return a 0–100 value representing the SLI percentage. Windows: `1d`, `7d`, `30d`.

### Anomaly Detection

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/anomalies` | List recent anomaly events (newest first) |
| `GET` | `/api/v1/anomalies?metric=<name>` | Filter anomaly events by metric name |

```bash
# All recent anomalies
curl http://localhost:9090/api/v1/anomalies

# Filter by metric
curl "http://localhost:9090/api/v1/anomalies?metric=cpu_usage_percent"
```

Response entries include `metric_name`, `labels`, `value`, `expected_value`, `deviation_score`, and `severity` (`low`/`medium`/`high`).

### Metric Correlation

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/correlate?a=<m>&b=<m>&window=<w>` | Pearson correlation between two metrics |
| `GET` | `/api/v1/correlate/auto?metric=<m>&window=<w>` | Top 5 most correlated metrics |

```bash
# Correlation between CPU and memory over 30 minutes
curl "http://localhost:9090/api/v1/correlate?a=cpu_usage_percent&b=memory_usage_percent&window=30m"

# Auto-discover metrics correlated with CPU over 1 hour
curl "http://localhost:9090/api/v1/correlate/auto?metric=cpu_usage_percent&window=1h"
```

Valid `window` values: `5m` `15m` `30m` `1h` `6h` `1d`

### Resource Forecasting & Capacity

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/forecast?metric=<name>` | Linear-regression forecast (1h/24h/7d predictions + trend + R²) |
| `GET` | `/api/v1/forecast?metric=<name>&threshold=<value>` | Exhaustion estimate — time until metric reaches threshold |
| `GET` | `/api/v1/capacity` | Full capacity report for CPU, Memory, and Disk |

### Downsampling & Retention

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/retention` | List all retention policies (built-in + custom) |
| `POST` | `/api/v1/retention` | Create a custom retention policy |
| `DELETE` | `/api/v1/retention/{name}` | Delete a custom retention policy |

### Admin

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/admin/status` | System status (uptime, Go version, goroutines, heap, TSDB stats) |
| `POST` | `/api/v1/admin/gc` | Force TSDB + Go runtime garbage collection |
| `POST` | `/api/v1/admin/snapshot` | Flush TSDB to disk immediately |
| `GET` | `/api/v1/admin/config` | Running config (API keys redacted) |
| `POST` | `/api/v1/admin/reload` | Hot-reload `watchtower.yaml` (server/agent/retention fields) |

```bash
# System status
curl http://localhost:9090/api/v1/admin/status

# Force GC
curl -X POST http://localhost:9090/api/v1/admin/gc

# List retention policies
curl http://localhost:9090/api/v1/retention

# Add a custom 2-hour retention policy for debug metrics
curl -X POST http://localhost:9090/api/v1/retention \
  -H 'Content-Type: application/json' \
  -d '{"name":"debug-short","match_pattern":"^debug_.*","max_age_seconds":7200}'

# Delete a custom policy
curl -X DELETE http://localhost:9090/api/v1/retention/debug-short
```

```bash
# Forecast CPU for 1h/24h/7d
curl "http://localhost:9090/api/v1/forecast?metric=cpu_usage_percent"

# Predict when disk will reach 90%
curl "http://localhost:9090/api/v1/forecast?metric=disk_usage_percent&threshold=90"

# Full capacity planning report
curl "http://localhost:9090/api/v1/capacity"
```

### Process Monitor

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/processes` | List top 20 processes by CPU (add `?sort=memory` for memory order) |

```bash
# Top processes by CPU
curl http://localhost:9090/api/v1/processes

# Top processes by memory
curl "http://localhost:9090/api/v1/processes?sort=memory"
```

### Status Page

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/status` | Public HTML status page (no auth required) |
| `GET` | `/status/badge` | SVG badge: green=operational, yellow=degraded, red=down |

```bash
# View status page
curl http://localhost:9090/status

# Get SVG badge (embed in a README with ![status](http://localhost:9090/status/badge))
curl http://localhost:9090/status/badge
```

### Aggregation Pipeline

| Method | Path | Description |
|--------|------|-------------|
| `GET`    | `/api/v1/pipeline/rules` | List all aggregation rules |
| `POST`   | `/api/v1/pipeline/rules` | Create a new aggregation rule |
| `DELETE` | `/api/v1/pipeline/rules/{name}` | Remove a rule |

```bash
# Compute p95 request latency per service, every 5 minutes
curl -X POST http://localhost:9090/api/v1/pipeline/rules \
  -H "Content-Type: application/json" \
  -d '{
    "name":          "latency_p95",
    "input_metric":  "request_latency_ms",
    "output_metric": "request_latency_ms_p95",
    "aggregation":   "p95",
    "window":        "5m",
    "group_by":      ["service"]
  }'

# List rules
curl http://localhost:9090/api/v1/pipeline/rules

# Delete a rule
curl -X DELETE http://localhost:9090/api/v1/pipeline/rules/latency_p95
```

Valid `aggregation` values: `avg` `sum` `max` `min` `count` `p50` `p95` `p99`
Valid `window` values: `1m` `5m` `1h`

### Data Export

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/export/metrics` | Export metrics as CSV or JSON |
| `GET` | `/api/v1/export/logs` | Export logs as CSV or JSON |
| `GET` | `/api/v1/export/traces` | Export traces as CSV or JSON |

Query params:
- `format` — `csv` (default `json`)
- `name` — metric name filter (metrics only; omit for all metrics)
- `level` — log level filter (logs only)
- `source` — log source filter (logs only)
- `start`, `end` — Unix millisecond timestamps (default: past 1 hour)

```bash
# Download last hour of CPU metrics as CSV
curl "http://localhost:9090/api/v1/export/metrics?format=csv&name=cpu_usage_percent" -o cpu.csv

# Download all logs as JSON
curl "http://localhost:9090/api/v1/export/logs?format=json" -o logs.json

# Export traces for a specific window
START=$(date -d '1 hour ago' +%s)000
END=$(date +%s)000
curl "http://localhost:9090/api/v1/export/traces?format=csv&start=${START}&end=${END}" -o traces.csv
```

### WebSocket `/ws`

Connected clients receive a push every 5 seconds:

```json
{
  "metrics": {
    "cpu_usage_percent": 45.2,
    "memory_usage_percent": 62.1,
    "memory_used_bytes": 10234961920,
    "disk_usage_percent": 38.5
  },
  "alerts": [...],
  "logs": [...],
  "ts": 1711000000000
}
```

## Project Structure

```
watchtower/
├── cmd/watchtower/
│   └── main.go                  # Entry point — wires all components
├── internal/
│   ├── config/
│   │   ├── config.go            # YAML config: Default(), Load(), applyDefaults()
│   │   └── config_test.go
│   ├── agent/
│   │   ├── agent.go             # System metrics agent (gopsutil)
│   │   ├── log_agent.go         # Log agent (synthetic system events)
│   │   ├── trace_agent.go       # Trace agent (synthetic 4-span traces, 20% error rate)
│   │   └── agent_test.go
│   ├── tsdb/
│   │   ├── tsdb.go              # In-memory time-series database
│   │   ├── series.go            # Single time series
│   │   ├── storage.go           # Gorilla chunk persistence (Delta-of-Delta + XOR)
│   │   ├── tsdb_test.go
│   │   └── storage_test.go
│   ├── wql/
│   │   ├── lexer.go             # WQL lexer
│   │   ├── parser.go            # WQL parser (AST)
│   │   ├── evaluator.go         # WQL evaluator
│   │   ├── lexer_test.go
│   │   ├── parser_test.go
│   │   └── evaluator_test.go
│   ├── alert/
│   │   ├── rule.go              # AlertRule, Alert, AlertState, AlertEvent types
│   │   ├── engine.go            # Evaluation loop, state machine, webhook dispatch
│   │   ├── api.go               # Alert HTTP API
│   │   ├── rule_test.go
│   │   ├── engine_test.go
│   │   └── api_test.go
│   ├── logstore/
│   │   ├── store.go             # Ring-buffer log store, full-text + regex search
│   │   └── store_test.go
│   ├── probe/
│   │   ├── probe.go             # ProbeManager, per-probe goroutines, TSDB write
│   │   ├── api.go               # Probe HTTP API
│   │   └── probe_test.go
│   ├── tracestore/
│   │   ├── store.go             # In-memory trace store, ring-buffer eviction, 1h retention
│   │   ├── api.go               # Trace HTTP API (ingest, list, get)
│   │   └── store_test.go
│   ├── auth/
│   │   ├── auth.go              # APIKey, KeyStore, Middleware, GenerateKey
│   │   ├── api.go               # Key management HTTP API (/api/v1/auth/keys/...)
│   │   └── auth_test.go
│   ├── registry/
│   │   ├── registry.go          # Agent registry: thread-safe map, offline detection
│   │   ├── api.go               # Agent CRUD HTTP API (/api/v1/agents/...)
│   │   └── registry_test.go
│   ├── ingest/
│   │   ├── server.go            # Ingest HTTP server (implements http.Handler for auth)
│   │   ├── prometheus.go        # Prometheus text format parser + scrape endpoint
│   │   ├── server_test.go
│   │   ├── prometheus_test.go
│   │   └── log_api_test.go
│   ├── dashboard/
│   │   ├── dashboard.go         # WebSocket server + static file handler
│   │   ├── panels.go            # Custom panel store + panel CRUD HTTP API
│   │   └── static/
│   │       └── index.html       # Single-page dashboard (6 tabs + custom panels)
│   └── model/
│       ├── metric.go            # MetricPoint type + fingerprint
│       ├── log.go               # LogEntry, LogLevel types
│       ├── trace.go             # Span, Trace, TraceSummary, SpanLog types
│       └── metric_test.go
├── watchtower.yaml              # Example configuration
└── watchtower-data/
    └── chunks/                  # Gorilla chunk files (*.wtkc, created at runtime)
```

## Running Tests

```bash
go test ./...                        # all packages (~330 tests)
go test ./internal/auth/...          # auth middleware + key management (19 tests)
go test ./internal/ingest/...        # ingest server + Prometheus parser (12 new)
go test ./internal/registry/...      # agent registry + API (15 tests)
go test ./internal/tracestore/...    # trace store + API (13 tests)
go test ./internal/probe/...         # endpoint probing (13 tests)
go test ./internal/config/...        # config loading (8 tests)
go test ./internal/logstore/...      # log store
go test ./internal/alert/...         # alert engine
go test ./internal/wql/...           # WQL language
go test ./internal/tsdb/...          # TSDB + persistence
```

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.21+ |
| System metrics | [gopsutil/v3](https://github.com/shirou/gopsutil) |
| WebSocket | [gorilla/websocket](https://github.com/gorilla/websocket) |
| YAML config | [gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3) |
| Distributed tracing | Custom span/trace model; waterfall diagram in browser |
| Frontend charts | [Chart.js 4](https://www.chartjs.org/) |
| Query language | WQL (custom, PromQL-inspired) |
| Storage | In-memory TSDB + Gorilla compressed chunks (no external dependencies) |
