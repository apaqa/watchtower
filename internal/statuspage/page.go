// Package statuspage 提供面向用户的公开状态页面：
// GET /status  — HTML 状态页（无需认证）
// GET /status/badge — SVG 状态徽章
package statuspage

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/slo"
)

// OverallStatus 描述系统整体运行状态
type OverallStatus string

const (
	StatusOperational OverallStatus = "operational"
	StatusDegraded    OverallStatus = "degraded"
	StatusDown        OverallStatus = "down"
)

// ProbeStatusProvider 抽象探针状态来源，方便测试时注入 mock
type ProbeStatusProvider interface {
	GetStatus() []probe.ProbeStatus
}

// Handler 持有生成状态页面所需的数据源引用
type Handler struct {
	probeMgr ProbeStatusProvider
	sloStore *slo.Store
	alertEng *alert.Engine
}

// New 创建状态页处理器（pm/ss/ae 均可为 nil）
func New(pm ProbeStatusProvider, ss *slo.Store, ae *alert.Engine) *Handler {
	return &Handler{probeMgr: pm, sloStore: ss, alertEng: ae}
}

// RegisterRoutes 注册 /status 和 /status/badge 路由到给定的 ServeMux
// 这两个端点不受 API 密钥认证保护（公开访问）
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("/status", h.handleStatusPage)
	mux.HandleFunc("/status/badge", h.handleBadge)
}

// overall 根据探针状态计算整体系统健康度
func (h *Handler) overall() OverallStatus {
	if h.probeMgr == nil {
		return StatusOperational
	}
	statuses := h.probeMgr.GetStatus()
	if len(statuses) == 0 {
		return StatusOperational
	}
	downCount := 0
	for _, s := range statuses {
		if s.Status == "down" {
			downCount++
		}
	}
	if downCount == 0 {
		return StatusOperational
	}
	if downCount == len(statuses) {
		return StatusDown
	}
	return StatusDegraded
}

// handleBadge 返回 SVG 格式的状态徽章（绿色/黄色/红色）
func (h *Handler) handleBadge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := h.overall()
	color := "#3fb950" // 绿色：operational
	switch status {
	case StatusDegraded:
		color = "#d29922" // 黄色
	case StatusDown:
		color = "#f85149" // 红色
	}
	label := string(status)
	labelWidth := len(label)*7 + 10
	totalWidth := 90 + labelWidth

	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20">
  <linearGradient id="s" x2="0" y2="100%%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <rect rx="3" width="%d" height="20" fill="#555"/>
  <rect rx="3" x="62" width="%d" height="20" fill="%s"/>
  <rect rx="3" width="%d" height="20" fill="url(#s)"/>
  <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
    <text x="31" y="15" fill="#010101" fill-opacity=".3">status</text>
    <text x="31" y="14">status</text>
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
  </g>
</svg>`,
		totalWidth, totalWidth, labelWidth, color, totalWidth,
		62+labelWidth/2, label,
		62+labelWidth/2, label)

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, svg)
}

// pageData 是传给 HTML 模板的渲染数据
type pageData struct {
	Overall   OverallStatus
	Probes    []probe.ProbeStatus
	SLOs      []slo.SLOStatus
	Incidents []incidentItem
	Now       string
}

// incidentItem 表示一条事件记录（由告警触发）
type incidentItem struct {
	Name      string
	Message   string
	State     string
	Timestamp time.Time
}

var statusTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"upper": strings.ToUpper,
	"statusClass": func(s string) string {
		switch s {
		case "up", "operational":
			return "ok"
		case "degraded":
			return "warn"
		case "down":
			return "err"
		default:
			return "warn"
		}
	},
	"stateClass": func(s string) string {
		switch s {
		case "resolved", "inactive":
			return "ok"
		case "firing":
			return "err"
		default:
			return "warn"
		}
	},
	"fmtTime": func(t time.Time) string {
		return t.Format("2006-01-02 15:04:05")
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>WatchTower Status</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#0d1117;color:#c9d1d9;min-height:100vh;padding:2rem 1rem}
.container{max-width:800px;margin:0 auto}
h1{font-size:1.8rem;font-weight:700;margin-bottom:0.3rem}
.subtitle{color:#8b949e;font-size:0.9rem;margin-bottom:2rem}
.overall{display:flex;align-items:center;gap:0.75rem;padding:1rem 1.25rem;border-radius:8px;margin-bottom:2rem;font-size:1.05rem;font-weight:600}
.overall.ok{background:#0d2a0d;border:1px solid #238636}
.overall.warn{background:#2a1f0d;border:1px solid #d29922}
.overall.err{background:#2a0d0d;border:1px solid #f85149}
.dot{width:12px;height:12px;border-radius:50%;flex-shrink:0}
.dot.ok{background:#3fb950}
.dot.warn{background:#d29922}
.dot.err{background:#f85149}
section{margin-bottom:2rem}
h2{font-size:1rem;font-weight:600;color:#8b949e;text-transform:uppercase;letter-spacing:.05em;margin-bottom:0.75rem}
.card{background:#161b22;border:1px solid #30363d;border-radius:8px;overflow:hidden}
.row{display:flex;align-items:center;justify-content:space-between;padding:0.7rem 1rem;border-bottom:1px solid #21262d}
.row:last-child{border-bottom:none}
.row-name{font-weight:500}
.row-meta{color:#8b949e;font-size:0.82rem;margin-top:2px}
.badge{display:inline-block;padding:2px 10px;border-radius:10px;font-size:0.78rem;font-weight:600}
.badge.ok{background:#0d2a0d;color:#3fb950;border:1px solid #238636}
.badge.warn{background:#2a1f0d;color:#d29922;border:1px solid #9e6a03}
.badge.err{background:#2a0d0d;color:#f85149;border:1px solid #b91c1c}
.uptime{color:#8b949e;font-size:0.82rem}
.incident-time{color:#8b949e;font-size:0.8rem;white-space:nowrap;margin-left:1rem}
.empty{color:#8b949e;font-size:0.85rem;padding:1rem;text-align:center}
footer{color:#8b949e;font-size:0.8rem;text-align:center;margin-top:3rem}
</style>
</head>
<body>
<div class="container">
  <h1>WatchTower Status</h1>
  <div class="subtitle">Updated {{.Now}}</div>

  <!-- 整体状态 -->
  <div class="overall {{statusClass (print .Overall)}}">
    <div class="dot {{statusClass (print .Overall)}}"></div>
    All Systems {{upper (print .Overall)}}
  </div>

  <!-- 端点状态 -->
  {{if .Probes}}
  <section>
    <h2>Monitored Endpoints</h2>
    <div class="card">
      {{range .Probes}}
      <div class="row">
        <div>
          <div class="row-name">{{.Name}}</div>
          <div class="row-meta">{{.URL}}</div>
        </div>
        <div style="text-align:right">
          <span class="badge {{statusClass .Status}}">{{upper .Status}}</span>
          <div class="uptime">Uptime {{printf "%.1f" .UptimePct}}% · {{.ResponseMs}}ms</div>
        </div>
      </div>
      {{end}}
    </div>
  </section>
  {{end}}

  <!-- SLO 状态 -->
  {{if .SLOs}}
  <section>
    <h2>SLO Status</h2>
    <div class="card">
      {{range .SLOs}}
      <div class="row">
        <div>
          <div class="row-name">{{.SLO.Name}}</div>
          <div class="row-meta">Target: {{printf "%.2f" .SLO.TargetPercent}}%</div>
        </div>
        <div style="text-align:right">
          {{if .Healthy}}
          <span class="badge ok">HEALTHY</span>
          {{else}}
          <span class="badge err">BREACHED</span>
          {{end}}
          <div class="uptime">SLI: {{printf "%.2f" .CurrentSLI}}%</div>
        </div>
      </div>
      {{end}}
    </div>
  </section>
  {{end}}

  <!-- 近期事件 -->
  <section>
    <h2>Recent Incidents</h2>
    {{if .Incidents}}
    <div class="card">
      {{range .Incidents}}
      <div class="row">
        <div>
          <div class="row-name">{{.Name}}</div>
          <div class="row-meta">{{.Message}}</div>
        </div>
        <div style="display:flex;align-items:center">
          <span class="badge {{stateClass .State}}">{{upper .State}}</span>
          <span class="incident-time">{{fmtTime .Timestamp}}</span>
        </div>
      </div>
      {{end}}
    </div>
    {{else}}
    <div class="card"><div class="empty">No incidents recorded</div></div>
    {{end}}
  </section>

  <footer>Powered by WatchTower</footer>
</div>
</body>
</html>`))

// handleStatusPage 渲染 HTML 状态页
func (h *Handler) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data := pageData{
		Overall: h.overall(),
		Now:     time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	}

	if h.probeMgr != nil {
		data.Probes = h.probeMgr.GetStatus()
	}

	if h.sloStore != nil {
		data.SLOs = h.sloStore.ListStatuses()
	}

	if h.alertEng != nil {
		history := h.alertEng.GetHistory()
		// 保留最近 10 条 firing 或 resolved 事件作为事件列表
		for _, ev := range history {
			if ev.State == alert.StateFiring || ev.State == alert.StateResolved {
				data.Incidents = append(data.Incidents, incidentItem{
					Name:      ev.RuleName,
					Message:   ev.Message,
					State:     string(ev.State),
					Timestamp: ev.Timestamp,
				})
			}
			if len(data.Incidents) >= 10 {
				break
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}
