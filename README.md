# WatchTower 监控平台

WatchTower 是一个轻量级、自包含的系统监控平台，使用 Go 编写，无需任何外部数据库或消息队列。单个二进制文件即可完成指标采集、存储和可视化全流程。

## 功能特性

- **系统代理**：每 5 秒采集 CPU、内存、磁盘使用率并推送到本地摄入服务
- **HTTP 摄入 API**：`POST /api/v1/metrics` 接收 JSON 格式指标数据
- **内存时间序列数据库**：自动清理 1 小时前的旧数据，线程安全
- **实时 Web 仪表板**：通过 WebSocket 推送，Chart.js 实时折线图，深色主题

## 架构图

```
┌─────────────────────────────────────────────────────────┐
│                    WatchTower 进程                        │
│                                                          │
│  ┌──────────┐   POST /api/v1/metrics   ┌─────────────┐  │
│  │  Agent   │ ─────────────────────▶  │ Ingest :9090│  │
│  │(gopsutil)│                          └──────┬──────┘  │
│  └──────────┘                                 │ Write   │
│                                               ▼         │
│                                        ┌─────────────┐  │
│                                        │  In-Memory  │  │
│                                        │    TSDB     │  │
│                                        └──────┬──────┘  │
│                                               │ Query   │
│                                               ▼         │
│                                    ┌──────────────────┐ │
│  Browser ◀──── WebSocket ──────── │ Dashboard :8080  │ │
│           ◀──── HTML/JS  ──────── │  (Chart.js UI)   │ │
│                                    └──────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## 目录结构

```
watchtower/
├── cmd/watchtower/
│   └── main.go                  # 程序入口，启动所有组件
├── internal/
│   ├── agent/
│   │   ├── agent.go             # 系统指标采集代理
│   │   └── agent_test.go
│   ├── dashboard/
│   │   ├── dashboard.go         # Web UI + WebSocket 服务
│   │   └── static/
│   │       └── index.html       # 单页仪表板（嵌入二进制）
│   ├── ingest/
│   │   ├── server.go            # HTTP 摄入端点
│   │   └── server_test.go
│   ├── model/
│   │   ├── metric.go            # 共享数据类型 + 指纹算法
│   │   └── metric_test.go
│   └── tsdb/
│       ├── tsdb.go              # 时序数据库主体
│       ├── series.go            # 单条时间序列
│       └── tsdb_test.go
├── go.mod
├── go.sum
└── README.md
```

## 快速开始

**前提**：Go 1.21+

```bash
# 克隆项目
git clone https://github.com/apaqa/watchtower.git
cd watchtower

# 拉取依赖
go mod download

# 编译并运行
go run ./cmd/watchtower

# 或者先构建再运行
go build -o watchtower ./cmd/watchtower
./watchtower
```

启动后访问：
- **仪表板**: http://localhost:8080
- **摄入 API**: http://localhost:9090/api/v1/metrics

### 手动推送自定义指标

```bash
curl -X POST http://localhost:9090/api/v1/metrics \
  -H "Content-Type: application/json" \
  -d '[{"name":"my_metric","labels":{"host":"server1"},"value":42.5}]'
```

## 运行测试

```bash
go test ./...
```

## 仪表板截图

> _TODO: 在此处插入仪表板截图_
>
> ![WatchTower Dashboard](docs/screenshot.png)

## 技术栈

| 组件 | 技术 |
|------|------|
| 语言 | Go 1.21+ |
| 系统指标 | [gopsutil/v3](https://github.com/shirou/gopsutil) |
| WebSocket | [gorilla/websocket](https://github.com/gorilla/websocket) |
| 前端图表 | [Chart.js 4](https://www.chartjs.org/) |
| 存储 | 纯内存（无外部依赖） |

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
