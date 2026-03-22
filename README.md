# WatchTower 监控平台

WatchTower 是一个轻量级、自包含的系统监控平台，使用 Go 编写，无需任何外部数据库或消息队列。单个二进制文件即可完成指标采集、存储、持久化和可视化全流程。

## 功能特性

- **系统代理**：每 5 秒采集 CPU、内存、磁盘使用率并推送到本地摄入服务
- **HTTP 摄入 API**：`POST /api/v1/metrics` 接收 JSON 格式指标数据
- **WQL 查询语言**：`GET /api/v1/query?q=avg(cpu_usage_percent[5m])` 实时查询时序数据
- **告警引擎**：可配置规则 + 状态机（Inactive→Pending→Firing→Resolved）+ Webhook 通知
- **告警 REST API**：CRUD 管理告警规则，查询活跃告警和历史记录
- **日志采集**：内存环形缓冲区（每来源 10000 条）+ 24 小时自动清理，支持全文和正则搜索
- **日志摄入 API**：`POST /api/v1/logs`（批量）/ `POST /api/v1/logs/single`（单条）/ `GET /api/v1/logs`（搜索）
- **日志代理**：每 10 秒生成一条合成系统事件日志（可替换为真实日志文件采集）
- **TSDB 磁盘持久化**：数据以压缩块文件（Gorilla 编码）存储于 `watchtower-data/chunks/`，重启后自动恢复
- **内存时间序列数据库**：自动清理 1 小时前的旧数据，线程安全
- **实时 Web 仪表板**：WebSocket 推送 + Chart.js 实时折线图 + WQL 查询界面 + 告警管理面板 + 日志查看器，深色主题，标签式导航

## 架构图

```
┌─────────────────────────────────────────────────────────────┐
│                      WatchTower 进程                         │
│                                                              │
│  ┌──────────┐   POST /api/v1/metrics   ┌─────────────────┐  │
│  │  Agent   │ ─────────────────────▶  │  Ingest  :9090  │  │
│  │(gopsutil)│                          │ GET /api/v1/query│  │
│  └──────────┘                          └────────┬────────┘  │
│                                                 │ Write/WQL │
│                                                 ▼           │
│                                     ┌───────────────────┐   │
│                                     │    In-Memory TSDB │   │
│                                     │  + Gorilla Chunks │   │
│                                     │  (watchtower-data)│   │
│                                     └─────────┬─────────┘   │
│                                               │ Query        │
│                                               ▼              │
│                                   ┌────────────────────────┐ │
│  Browser ◀──── WebSocket ──────  │   Dashboard  :8080     │ │
│           ◀──── HTML/JS  ──────  │ Chart.js + WQL 查询框  │ │
│                                   └────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

## 目录结构

```
watchtower/
├── cmd/watchtower/
│   └── main.go                  # 程序入口，启动所有组件
├── internal/
│   ├── agent/
│   │   ├── agent.go             # 系统指标采集代理
│   │   ├── log_agent.go         # 日志采集代理（合成系统事件日志）
│   │   └── agent_test.go
│   ├── dashboard/
│   │   ├── dashboard.go         # Web UI + WebSocket 服务
│   │   └── static/
│   │       └── index.html       # 单页仪表板（指标/日志/告警三标签式导航）
│   ├── ingest/
│   │   ├── server.go            # HTTP 摄入端点 + WQL 查询 API + 日志 API
│   │   ├── server_test.go
│   │   └── log_api_test.go
│   ├── model/
│   │   ├── metric.go            # 共享指标数据类型 + 指纹算法
│   │   ├── log.go               # 日志数据类型：LogEntry、LogLevel、LogFingerprint
│   │   └── metric_test.go
│   ├── logstore/
│   │   ├── store.go             # 内存环形缓冲区日志存储，支持全文/正则搜索
│   │   └── store_test.go
│   ├── alert/
│   │   ├── rule.go              # AlertRule、Alert、AlertState、AlertEvent 类型定义
│   │   ├── engine.go            # 告警引擎：评估循环、状态机、Webhook 通知
│   │   ├── api.go               # 告警 HTTP API 路由注册与处理器
│   │   ├── rule_test.go
│   │   ├── engine_test.go
│   │   └── api_test.go
│   ├── tsdb/
│   │   ├── tsdb.go              # 时序数据库主体
│   │   ├── series.go            # 单条时间序列
│   │   ├── storage.go           # Gorilla 压缩块持久化（Delta-of-Delta + XOR）
│   │   ├── tsdb_test.go
│   │   └── storage_test.go
│   └── wql/
│       ├── lexer.go             # WQL 词法分析器
│       ├── parser.go            # WQL 语法分析器（AST）
│       ├── evaluator.go         # WQL 求值器（对 TSDB 求值）
│       ├── lexer_test.go
│       ├── parser_test.go
│       └── evaluator_test.go
└── watchtower-data/
    └── chunks/                  # Gorilla 压缩块文件（*.wtkc，运行时生成）
```

## 快速开始

**前提**：Go 1.21+

```bash
# 克隆项目
git clone https://github.com/apaqa/watchtower.git
cd watchtower

# 拉取依赖
go mod download

# 编译并运行（自动在 watchtower-data/ 目录持久化数据）
go run ./cmd/watchtower

# 或者先构建再运行
go build -o watchtower ./cmd/watchtower
./watchtower
```

启动后访问：
- **仪表板**: http://localhost:8080
- **摄入 API**: http://localhost:9090/api/v1/metrics
- **WQL 查询**: http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])
- **告警规则**: http://localhost:9090/api/v1/alerts/rules
- **活跃告警**: http://localhost:9090/api/v1/alerts/active
- **日志搜索**: http://localhost:9090/api/v1/logs?q=error&level=error&limit=50

### 手动推送自定义指标

```bash
curl -X POST http://localhost:9090/api/v1/metrics \
  -H "Content-Type: application/json" \
  -d '[{"name":"my_metric","labels":{"host":"server1"},"value":42.5}]'
```

## WQL 查询语言

WQL（WatchTower Query Language）是受 PromQL 启发的轻量级时序查询语言。

### 语法参考

```
# 聚合函数 + 时间范围
avg(cpu_usage_percent[5m])
max(memory_usage_percent[1h])
min(disk_usage_percent[15m])
sum(memory_used_bytes[1m])
count(cpu_usage_percent[5m])
rate(cpu_usage_percent[5m])    # 每秒增长率
last(cpu_usage_percent[5m])    # 最新值

# 标签过滤器
avg(cpu_usage_percent{host="DESKTOP"}[5m])
max(cpu_usage_percent{host="server1",env="prod"}[1h])

# 时间范围
[1m]   [5m]   [15m]   [1h]   [1d]

# 算术运算
avg(memory_used_bytes[5m]) / 1073741824       # 转换为 GB
avg(cpu_usage_percent[5m]) * 2

# 比较运算（返回 true/false）
avg(cpu_usage_percent[5m]) > 80
max(memory_usage_percent[5m]) >= 90

# 分组聚合（by 子句）
sum(cpu_usage_percent[5m]) by (host)
avg(memory_usage_percent[5m]) by (host)
```

### HTTP API

```bash
# 标量查询
curl "http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])"
# 返回：{"type":"scalar","scalar":45.23}

# 向量查询（多主机）
curl "http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])%20by%20(host)"
# 返回：{"type":"vector","vector":[{"labels":{"host":"DESKTOP"},"value":45.23}]}

# 比较查询
curl "http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])%20>%2080"
# 返回：{"type":"bool","bool":false}
```

## TSDB 持久化

WatchTower 使用 **Gorilla 论文**中的两种无损压缩算法将数据持久化到磁盘：

| 编码算法 | 用途 | 压缩原理 |
|---------|------|---------|
| **Delta-of-Delta** | 时间戳 | 存储"差值的差值"，对均匀采样数据极为高效（DoD=0 仅占 1 位） |
| **XOR 编码** | 浮点值 | 与前值 XOR 后只存储变化的有效位，相邻值相近时压缩率极高 |

### 块文件格式

- 文件路径：`watchtower-data/chunks/{fingerprint}_{start_ms}.wtkc`
- 每个块存储一条序列最多 **1 小时**的数据
- 后台每 **30 秒**自动刷盘，进程退出时执行最终刷盘
- 启动时自动从磁盘**恢复所有历史数据**

## 告警引擎

### 创建告警规则

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

### 告警状态机

```
Inactive ──(条件满足, duration=0)──▶ Firing ──(条件消除)──▶ Resolved ──▶ Inactive
Inactive ──(条件满足, duration>0)──▶ Pending ──(持续足够长)──▶ Firing
                                   └──(条件消除)──▶ Inactive
```

| 状态 | 含义 |
|------|------|
| `inactive` | 条件为假，告警未激活 |
| `pending` | 条件为真，等待 duration 时长到达 |
| `firing` | 告警已触发，发送 Firing Webhook |
| `resolved` | 条件恢复为假，发送 Resolved Webhook |

### Webhook 通知载荷

```json
{
  "alert_name": "high_cpu",
  "severity": "critical",
  "value": 95.23,
  "message": "avg(cpu_usage_percent[5m]) 当前值 95.2300（阈值：> 80.0000）",
  "state": "firing",
  "fired_at": "2026-03-22T10:00:00Z"
}
```

### 告警 API 参考

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/v1/alerts/rules` | 创建告警规则 |
| `GET` | `/api/v1/alerts/rules` | 列出所有规则及状态 |
| `DELETE` | `/api/v1/alerts/rules/{name}` | 删除指定规则 |
| `GET` | `/api/v1/alerts/active` | 当前 Firing 告警列表 |
| `GET` | `/api/v1/alerts/history` | 最近 100 条状态变化记录 |

## 日志采集

### 写入日志

```bash
# 批量写入（JSON 数组）
curl -X POST http://localhost:9090/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '[
    {"level":"info",  "source":"myapp","message":"server started"},
    {"level":"error", "source":"myapp","message":"database connection failed"}
  ]'

# 单条写入
curl -X POST http://localhost:9090/api/v1/logs/single \
  -H "Content-Type: application/json" \
  -d '{"level":"warn","source":"myapp","message":"high memory usage detected"}'
```

### 搜索日志

```bash
# 全文搜索
curl "http://localhost:9090/api/v1/logs?q=error&limit=50"

# 级别过滤
curl "http://localhost:9090/api/v1/logs?level=error"

# 来源过滤 + 关键字
curl "http://localhost:9090/api/v1/logs?source=myapp&q=database"

# 正则搜索（以 / 包围的模式）
curl "http://localhost:9090/api/v1/logs?q=%2Ferror+code+%5Cd%2B%2F"
```

### 日志 API 参考

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/v1/logs` | 批量写入日志条目（JSON 数组） |
| `GET`  | `/api/v1/logs` | 搜索日志，支持 `q`、`level`、`source`、`limit` 参数 |
| `POST` | `/api/v1/logs/single` | 写入单条日志条目 |

### 日志查看器

访问仪表板 **http://localhost:8080** 后点击 **日志** 标签：

- 实时接收 WebSocket 推送的最新日志
- 顶部搜索栏支持关键字过滤（实时生效）和 `/regex/` 正则模式
- 级别下拉菜单快速筛选 ERROR / WARN / INFO / DEBUG
- 彩色级别徽章：ERROR=红、WARN=黄、INFO=蓝、DEBUG=灰
- 自动滚动到最新日志

### 日志数据结构

```json
{
  "timestamp": 1711000000000,
  "level": "error",
  "source": "myapp",
  "message": "database connection failed",
  "labels": {"host": "node1", "env": "prod"}
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `timestamp` | int64 | Unix 毫秒，可省略（服务器自动填充） |
| `level` | string | `debug` / `info` / `warn` / `error` |
| `source` | string | 来源标识（主机名或应用名），默认 `unknown` |
| `message` | string | 日志消息内容 |
| `labels` | object | 可选附加标签键值对 |

## 运行测试

```bash
go test ./...                     # 运行全部测试（91 个）
go test ./internal/logstore/...  # 仅运行日志存储测试
go test ./internal/alert/...     # 仅运行告警引擎测试
go test ./internal/wql/...       # 仅运行 WQL 测试
go test ./internal/tsdb/...      # 仅运行 TSDB 测试（含持久化测试）
```

## 技术栈

| 组件 | 技术 |
|------|------|
| 语言 | Go 1.21+ |
| 系统指标 | [gopsutil/v3](https://github.com/shirou/gopsutil) |
| WebSocket | [gorilla/websocket](https://github.com/gorilla/websocket) |
| 前端图表 | [Chart.js 4](https://www.chartjs.org/) |
| 查询语言 | WQL（自研，受 PromQL 启发） |
| 告警引擎 | 自研状态机，支持 Webhook 通知 |
| 存储 | 内存 TSDB + Gorilla 压缩块文件（无外部依赖） |

## API 参考

### `POST /api/v1/metrics`

请求体（JSON 数组）：

```json
[
  {
    "name": "cpu_usage_percent",
    "labels": { "host": "node1" },
    "value": 45.2,
    "timestamp": 1711000000000
  }
]
```

- `timestamp` 为 Unix 毫秒，可省略（服务器自动填充）
- 返回 `204 No Content` 表示成功

### `GET /api/v1/query?q=<WQL>`

返回 JSON 格式的查询结果：

```json
// 标量结果
{"type":"scalar","scalar":45.23}

// 向量结果（多序列）
{"type":"vector","vector":[
  {"labels":{"host":"A"},"value":30.0},
  {"labels":{"host":"B"},"value":90.0}
]}

// 布尔结果（比较运算）
{"type":"bool","bool":true}

// 错误
{"error":"指标 \"unknown\" 无数据"}
```

### WebSocket `/ws`

连接后每 5 秒收到推送：

```json
{
  "metrics": {
    "cpu_usage_percent": 45.2,
    "memory_usage_percent": 62.1,
    "memory_used_bytes": 10234961920,
    "disk_usage_percent": 38.5
  },
  "ts": 1711000000000
}
```
