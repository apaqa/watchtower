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

### Custom Dashboard Panels
- Users can define custom metric panels from any WQL expression via the dashboard UI
- In-memory `PanelStore` backed by ordered insertion; supports `stat` and `gauge` panel types
- Per-panel configurable auto-refresh interval (minimum 5 seconds, default 30 seconds)
- REST API at `:8080/api/v1/dashboard/panels`: `POST` to create, `GET` to list, `DELETE /{id}` to remove
- Panels persist for the lifetime of the process and appear in the Metrics tab "Custom Panels" section

### Dashboard
- Dark-theme single-page app served at `:8080`
- Live Chart.js line charts via WebSocket push (5-second interval)
- **Six tabs**: **Metrics** · **Logs** · **Alerts** · **Endpoints** · **Traces** · **Agents**
- WQL query box with instant results
- Log viewer with full-text/regex search and level filter
- Alert manager with rule creation modal and state history
- Endpoint cards with status badge, response time, uptime %, sparkline chart, and 20-check status dots
- Trace list with service filter and min-duration filter; click any trace to expand a proportional **waterfall diagram**
- **Agents tab**: table with hostname, IP, OS, status badge (online/offline), last-seen (relative), registered time; "X Online / Y Offline" summary; auto-refreshes every 15 seconds
- **Custom Panels** section in the Metrics tab: "+ Add Panel" button opens a modal; each panel auto-refreshes its WQL query at the configured interval

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
| Custom Panels API | http://localhost:8080/api/v1/dashboard/panels |

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
│   ├── registry/
│   │   ├── registry.go          # Agent registry: thread-safe map, offline detection
│   │   ├── api.go               # Agent CRUD HTTP API (/api/v1/agents/...)
│   │   └── registry_test.go
│   ├── ingest/
│   │   ├── server.go            # Ingest HTTP server
│   │   ├── server_test.go
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
go test ./...                        # all packages (~140 tests)
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
