# WatchTower

![Tests](https://img.shields.io/badge/tests-471%2B-brightgreen)
![Go](https://img.shields.io/badge/go-1.21%2B-00ADD8)
![License](https://img.shields.io/badge/license-MIT-blue)

A self-hosted observability platform built in Go. WatchTower collects metrics,
logs, and traces; evaluates alert rules; and serves a live dashboard — all from
a single binary with zero runtime dependencies.

---

## Features

| Category | Capabilities |
|----------|-------------|
| **Ingestion** | JSON metrics, Prometheus push/scrape, log batches, distributed traces, GitHub & generic webhooks |
| **Storage** | In-memory TSDB with WAL persistence, downsampling (1 m / 5 m tiers), configurable retention |
| **Query** | WQL — a time-series query language with aggregations, math, and label filtering |
| **Alerting** | Rule engine with WQL expressions, severity levels, state machine (pending → firing → normal), multi-channel notifications |
| **Monitoring** | HTTP endpoint probing, synthetic multi-step transactions, process & system metrics, Kubernetes health checks |
| **Observability** | Z-score anomaly detection, Pearson metric correlation, resource forecasting, capacity planning |
| **Security** | API-key auth, RBAC (admin / operator / viewer), per-tenant metric prefixing, audit log, token-bucket rate limiting |
| **Integrations** | Grafana SimpleJSON, on-call scheduling, incident management, plugin system (Network / GPU / Docker) |

---

## Architecture

```
  ┌─────────────────────────────────────────────────────────┐
  │                        Clients                          │
  │  curl / agent / Prometheus / GitHub webhook / browser   │
  └──────────────┬──────────────────────────┬───────────────┘
                 │ :9090                    │ :8080
     ┌───────────▼──────────┐   ┌───────────▼──────────┐
     │   Ingest Server      │   │  Dashboard Server     │
     │  Auth → RBAC →       │   │  WebSocket push       │
     │  Tenant → Audit →    │   │  Static SPA (tabs)    │
     │  Rate-limit → Mux    │   │  Share links /share/  │
     └───────────┬──────────┘   └──────────────────────-┘
                 │
     ┌───────────▼──────────────────────────────────────┐
     │                  Core Services                    │
     │  TSDB · LogStore · TraceStore · AlertEngine       │
     │  ProbeManager · SyntheticMonitor · AuditStore     │
     │  PipelineAggregator · AnomalyDetector · Forecaster│
     └───────────────────────────────────────────────────┘
```

---

## Quick Start

```bash
git clone https://github.com/apaqa/watchtower
cd watchtower
go run ./cmd/watchtower          # starts on :9090 (API) and :8080 (dashboard)
open http://localhost:8080        # live dashboard
```

Push your first metric:

```bash
curl -X POST http://localhost:9090/api/v1/metrics \
  -H 'Content-Type: application/json' \
  -d '[{"name":"cpu_pct","value":42.5,"timestamp":'$(date +%s000)'}]'
```

---

## Configuration

`watchtower.yaml` (all fields optional — sensible defaults apply):

```yaml
server:
  ingest_port: 9090
  dashboard_port: 8080

agent:
  enabled: true           # collect local system metrics every interval
  interval_seconds: 15

retention:
  max_age_seconds: 604800 # 7 days
  max_points_per_series: 10000

api_keys:
  - name: admin
    key: "change-me"
    role: admin            # admin | operator | viewer
    tenant_id: default

endpoints:                 # HTTP probes
  - name: api-gateway
    url: https://api.example.com/health
    interval_seconds: 30
    expected_status: 200

alerts:
  - name: high-cpu
    wql_expression: avg(cpu_usage_percent[5m])
    operator: ">"
    threshold: 85
    severity: warning

notifications:
  slack:
    enabled: true
    webhook_url: https://hooks.slack.com/services/XXX

synthetic_tests:
  - name: login-flow
    interval_seconds: 60
    timeout_ms: 5000
    steps:
      - name: get-token
        method: POST
        url: https://api.example.com/auth
        body: '{"user":"probe","pass":"secret"}'
        expected_status: 200
        extract_vars:
          token: $.access_token
      - name: fetch-profile
        url: https://api.example.com/me
        headers:
          Authorization: Bearer {token}
        assert_body_contains: '"email"'

webhooks:
  - name: myservice
    path: /api/v1/webhook/myservice
    extract_rules:
      - json_path: $.response_time
        metric_name: myservice_response_ms
        label_mappings:
          env: $.environment

plugins:
  - name: network
    enabled: true
  - name: docker
    enabled: true
```

---

## WQL Reference

WatchTower Query Language — a Prometheus-inspired expression language.

| Expression | Description |
|------------|-------------|
| `cpu_usage_percent` | Latest value of a metric |
| `cpu_usage_percent[5m]` | All points in last 5 minutes |
| `avg(cpu_usage_percent[5m])` | Average over window |
| `sum(net_bytes_sent[1m])` | Sum over window |
| `max(cpu_usage_percent[1h])` | Maximum |
| `min(mem_available_bytes[30m])` | Minimum |
| `p95(http_latency_ms[5m])` | 95th percentile |
| `rate(requests_total[1m])` | Per-second rate |
| `cpu_usage_percent > 80` | Threshold filter |
| `avg(cpu_usage_percent[5m]) * 100` | Arithmetic |

Use `:1m` or `:5m` suffixes to query downsampled tiers:
`avg(cpu_usage_percent:5m[1h])` → 5-minute rolled-up data over 1 hour.

---

## API Reference

### Metrics

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/metrics` | Ingest metric points (JSON array) |
| `GET`  | `/api/v1/query?q=<wql>` | Execute WQL query |
| `GET`  | `/api/v1/metrics/names` | List all metric names |
| `POST` | `/api/v1/metrics/prometheus` | Prometheus text-format push |
| `GET`  | `/metrics` | Prometheus scrape endpoint |

### Logs & Traces

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/logs` | Batch write log entries |
| `GET`  | `/api/v1/logs?q=<text>&level=<l>&limit=<n>` | Search logs |
| `POST` | `/api/v1/traces` | Ingest trace spans |
| `GET`  | `/api/v1/traces?service=<s>&limit=<n>` | Query traces |

### Alerts

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/v1/alerts/rules` | List alert rules |
| `POST` | `/api/v1/alerts/rules` | Create/update a rule |
| `DELETE` | `/api/v1/alerts/rules/{name}` | Delete a rule |
| `GET`  | `/api/v1/alerts` | Active alerts |
| `GET`  | `/api/v1/alerts/history` | Alert event history |

### Endpoint Probes & Synthetic Monitoring

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/v1/probes` | List probes and latest results |
| `POST` | `/api/v1/probes` | Add a probe |
| `DELETE` | `/api/v1/probes/{name}` | Remove a probe |
| `GET`  | `/api/v1/synthetic` | List synthetic tests |
| `POST` | `/api/v1/synthetic` | Register a new test |
| `GET`  | `/api/v1/synthetic/{name}/history` | Test run history |

### Webhooks

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/webhook/github` | GitHub events → log entries |
| `POST` | `/api/v1/webhook/generic?metric_name=X` | JSON → metrics |
| `POST` | `/api/v1/webhook/{custom}` | Configured endpoint |

### Security & Multi-Tenancy

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/v1/auth/keys` | List API keys (admin) |
| `POST` | `/api/v1/auth/keys` | Create API key |
| `DELETE` | `/api/v1/auth/keys/{name}` | Revoke key |
| `GET`  | `/api/v1/audit` | Audit log (admin) |
| `GET`  | `/api/v1/tenants` | List tenants |
| `POST` | `/api/v1/tenants` | Create tenant |
| `DELETE` | `/api/v1/tenants/{id}` | Delete tenant |

### Dashboard Sharing (port 8080)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/dashboard/share` | Create share token |
| `GET`  | `/api/v1/dashboard/shares` | List tokens |
| `DELETE` | `/api/v1/dashboard/shares/{token}` | Revoke |
| `GET`  | `/share/{token}` | Read-only shared view (HTML) |

### Operations

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/v1/health` | All health checks + status |
| `GET`  | `/api/v1/health/live` | Liveness probe (Kubernetes) |
| `GET`  | `/api/v1/health/ready` | Readiness probe (Kubernetes) |
| `GET`  | `/api/v1/quotas` | Resource quotas |
| `PUT`  | `/api/v1/quotas/{resource}` | Update quota limit |
| `GET`  | `/api/v1/plugins` | Plugin status |
| `POST` | `/api/v1/plugins/{name}/start` | Start a plugin |
| `POST` | `/api/v1/plugins/{name}/stop` | Stop a plugin |
| `GET`  | `/status` | Public status page (HTML + SVG badge) |
| `GET`  | `/api/v1/export/metrics` | Export metrics CSV / JSON |
| `GET`  | `/api/v1/export/logs` | Export logs CSV / JSON |

---

## Grafana Integration

Add WatchTower as a **SimpleJSON** data source in Grafana:

1. Install the *Grafana Simple JSON* plugin
2. Add data source → URL: `http://localhost:9090/api/grafana/`
3. All WQL metric names appear as available series in the query builder

---

## Plugin Development

Implement the `Plugin` interface to add custom metric collectors:

```go
type Plugin interface {
    Name() string
    Init(cfg map[string]interface{}) error
    Collect() ([]model.MetricPoint, error)
    Stop()
}
```

Register with the manager:

```go
mgr := plugin.New(func(pts []model.MetricPoint) { db.Write(pts) })
mgr.Register(myPlugin)
mgr.StartAll()
```

Built-in plugins: `network` (gopsutil), `gpu` (nvidia-smi), `docker` (docker stats).

---

## Project Structure

```
watchtower/
├── cmd/watchtower/main.go           # Entry point, startup banner, wiring
├── internal/
│   ├── tsdb/                        # Time-series DB (WAL, downsampling, retention)
│   ├── wql/                         # Query language parser & evaluator
│   ├── ingest/                      # HTTP ingest server, auth middleware chain
│   ├── alert/                       # Rule engine, state machine, history
│   ├── logstore/                    # Ring-buffer log store with search
│   ├── tracestore/                  # Span storage & query
│   ├── probe/                       # HTTP endpoint prober
│   ├── synthetic/                   # Multi-step transaction monitor
│   ├── webhook/                     # GitHub + generic + configured receivers
│   ├── agent/                       # System metrics / log / trace agents
│   ├── notify/                      # Multi-channel notification router
│   ├── anomaly/                     # Z-score anomaly detector
│   ├── correlation/                 # Pearson metric correlation
│   ├── forecast/                    # Linear regression forecaster
│   ├── plugin/                      # Plugin manager + Network/GPU/Docker
│   ├── auth/                        # API-key store, RBAC
│   ├── audit/                       # Audit log ring buffer
│   ├── tenant/                      # Multi-tenancy, metric prefixing
│   ├── quota/                       # Resource quotas + token-bucket limiter
│   ├── health/                      # Kubernetes-style health checks
│   ├── incident/                    # Incident store & escalation
│   ├── oncall/                      # On-call scheduling
│   ├── servicemap/                  # Service dependency graph
│   ├── slo/                         # SLO/SLI tracking & error budgets
│   ├── pipeline/                    # Metric aggregation pipeline rules
│   ├── export/                      # CSV/JSON data export
│   ├── grafana/                     # Grafana SimpleJSON data source API
│   ├── dashboard/                   # WebSocket server, SPA, share links
│   ├── statuspage/                  # Public status page + SVG badge
│   ├── procmon/                     # Process monitoring
│   ├── compare/                     # Period-over-period metric comparison
│   ├── replay/                      # Metric record & replay
│   ├── tags/                        # Tag management
│   ├── savedquery/                  # Saved WQL queries
│   ├── admin/                       # Admin API & system status
│   └── integration/                 # End-to-end integration tests (15 tests)
└── README.md
```

---

## Running Tests

```bash
go test ./...                        # all packages (471+ tests)
go test ./internal/integration/...  # 15 integration tests
go test ./internal/tsdb/... -v      # specific package, verbose
```
