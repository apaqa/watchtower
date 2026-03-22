# WatchTower

A lightweight, self-contained monitoring platform written in Go. A single binary collects system metrics, stores time-series data, evaluates alert rules, tails logs, and probes HTTP endpoints — all without external databases or message queues.

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                        WatchTower Process                         │
│                                                                   │
│  ┌───────────┐  POST /api/v1/metrics  ┌──────────────────────┐   │
│  │  System   │ ─────────────────────▶ │  Ingest API  :9090   │   │
│  │  Agent    │                        │  WQL Query API        │   │
│  │(gopsutil) │                        │  Alert API            │   │
│  └───────────┘                        │  Log API              │   │
│                                       │  Probe API            │   │
│  ┌───────────┐  POST /api/v1/logs     └──────────┬───────────┘   │
│  │  Log      │ ─────────────────────────────────▶│               │
│  │  Agent    │                                    │ Write / Query │
│  └───────────┘                                    ▼               │
│                                        ┌────────────────────┐    │
│  ┌───────────┐  probe_status metric    │   In-Memory TSDB   │    │
│  │  Endpoint │ ─────────────────────▶ │ + Gorilla Disk      │    │
│  │  Probes   │                        │   Persistence       │    │
│  └───────────┘                        └─────────┬──────────┘    │
│                                                  │ Query          │
│                                                  ▼                │
│                                   ┌──────────────────────────┐   │
│  Browser ◀─── WebSocket ────────  │   Dashboard  :8080       │   │
│           ◀─── HTML / JS ────────  │   Chart.js + WQL + Logs  │   │
│                                   └──────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
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

### Dashboard
- Dark-theme single-page app served at `:8080`
- Live Chart.js line charts via WebSocket push (5-second interval)
- Four tabs: **Metrics** · **Logs** · **Alerts** · **Endpoints**
- WQL query box with instant results
- Log viewer with full-text/regex search and level filter
- Alert manager with rule creation modal and state history
- Endpoint cards with status badge, response time, uptime %, sparkline chart, and 20-check status dots

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
│   ├── ingest/
│   │   ├── server.go            # Ingest HTTP server
│   │   ├── server_test.go
│   │   └── log_api_test.go
│   ├── dashboard/
│   │   ├── dashboard.go         # WebSocket server + static file handler
│   │   └── static/
│   │       └── index.html       # Single-page dashboard (Metrics/Logs/Alerts/Endpoints)
│   └── model/
│       ├── metric.go            # MetricPoint type + fingerprint
│       ├── log.go               # LogEntry, LogLevel types
│       └── metric_test.go
├── watchtower.yaml              # Example configuration
└── watchtower-data/
    └── chunks/                  # Gorilla chunk files (*.wtkc, created at runtime)
```

## Running Tests

```bash
go test ./...                      # all packages (~112 tests)
go test ./internal/config/...      # config loading (8 tests)
go test ./internal/probe/...       # endpoint probing (13 tests)
go test ./internal/logstore/...    # log store
go test ./internal/alert/...       # alert engine
go test ./internal/wql/...         # WQL language
go test ./internal/tsdb/...        # TSDB + persistence
```

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.21+ |
| System metrics | [gopsutil/v3](https://github.com/shirou/gopsutil) |
| WebSocket | [gorilla/websocket](https://github.com/gorilla/websocket) |
| YAML config | [gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3) |
| Frontend charts | [Chart.js 4](https://www.chartjs.org/) |
| Query language | WQL (custom, PromQL-inspired) |
| Storage | In-memory TSDB + Gorilla compressed chunks (no external dependencies) |
