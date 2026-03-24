// Package webhook 提供接收外部 Webhook 并将其转换为指标/日志的功能
package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// ExtractRule 描述从 Webhook JSON 负载中提取指标的规则
type ExtractRule struct {
	// JSONPath 是简化的点分路径，如 "$.status" 或 "$.data.count"
	JSONPath    string            `json:"json_path" yaml:"json_path"`
	MetricName  string            `json:"metric_name" yaml:"metric_name"`
	// LabelMappings 将标签名映射到 JSON 路径，如 {"repo":"$.repository.name"}
	LabelMappings map[string]string `json:"label_mappings" yaml:"label_mappings"`
}

// WebhookConfig 描述一个 Webhook 接收端点的配置
type WebhookConfig struct {
	Name  string       `json:"name" yaml:"name"`
	Path  string       `json:"path" yaml:"path"` // 注册到 ServeMux 的路径
	Rules []ExtractRule `json:"extract_rules" yaml:"extract_rules"`
}

// Handler 持有写入回调和已注册配置
type Handler struct {
	postMetrics func([]model.MetricPoint)
	writeLogs   func([]model.LogEntry)
	configs     []WebhookConfig
}

// New 创建一个 Webhook 处理器
func New(postMetrics func([]model.MetricPoint), writeLogs func([]model.LogEntry)) *Handler {
	return &Handler{
		postMetrics: postMetrics,
		writeLogs:   writeLogs,
	}
}

// AddConfig 注册一个自定义 Webhook 配置
func (h *Handler) AddConfig(c WebhookConfig) {
	h.configs = append(h.configs, c)
}

// RegisterRoutes 注册所有 Webhook API 路由到 ServeMux
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	// 内置 GitHub webhook 接收端
	mux.HandleFunc("/api/v1/webhook/github", h.handleGitHub)
	// 通用 JSON Webhook 接收端
	mux.HandleFunc("/api/v1/webhook/generic", h.handleGeneric)

	// 注册自定义配置的 Webhook 端点
	for _, cfg := range h.configs {
		c := cfg // 避免闭包捕获
		mux.HandleFunc(c.Path, func(w http.ResponseWriter, r *http.Request) {
			h.handleConfigured(w, r, c)
		})
	}
}

// ── GitHub Webhook ────────────────────────────────────────────────────────────

// githubPushPayload 只解析 GitHub Push 事件所需的字段
type githubPushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	Commits []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"commits"`
}

// githubPRPayload GitHub Pull Request 事件所需字段
type githubPRPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// githubIssuePayload GitHub Issues 事件所需字段
type githubIssuePayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// handleGitHub 处理 POST /api/v1/webhook/github
func (h *Handler) handleGitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"仅支持 POST"}`, http.StatusMethodNotAllowed)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		event = "unknown"
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"error":"JSON 解析失败"}`, http.StatusBadRequest)
		return
	}

	now := time.Now().UnixMilli()
	var entries []model.LogEntry

	switch event {
	case "push":
		var p githubPushPayload
		remarshal(raw, &p)
		msg := fmt.Sprintf("[github:push] %s pushed to %s (%d commits)",
			p.Pusher.Name, p.Repository.FullName, len(p.Commits))
		entries = append(entries, model.LogEntry{
			Timestamp: now,
			Level:     model.LogLevelInfo,
			Message:   msg,
			Source:    "github-webhook",
			Labels:    map[string]string{"event": "push", "repo": p.Repository.FullName, "ref": p.Ref},
		})

	case "pull_request":
		var p githubPRPayload
		remarshal(raw, &p)
		msg := fmt.Sprintf("[github:pr] %s PR#%d '%s' by %s",
			p.Action, p.Number, p.PullRequest.Title, p.PullRequest.User.Login)
		entries = append(entries, model.LogEntry{
			Timestamp: now,
			Level:     model.LogLevelInfo,
			Message:   msg,
			Source:    "github-webhook",
			Labels:    map[string]string{"event": "pull_request", "action": p.Action, "repo": p.Repository.FullName},
		})

	case "issues":
		var p githubIssuePayload
		remarshal(raw, &p)
		msg := fmt.Sprintf("[github:issue] %s issue#%d '%s' by %s",
			p.Action, p.Issue.Number, p.Issue.Title, p.Issue.User.Login)
		entries = append(entries, model.LogEntry{
			Timestamp: now,
			Level:     model.LogLevelInfo,
			Message:   msg,
			Source:    "github-webhook",
			Labels:    map[string]string{"event": "issues", "action": p.Action, "repo": p.Repository.FullName},
		})

	default:
		entries = append(entries, model.LogEntry{
			Timestamp: now,
			Level:     model.LogLevelInfo,
			Message:   fmt.Sprintf("[github:%s] webhook received", event),
			Source:    "github-webhook",
			Labels:    map[string]string{"event": event},
		})
	}

	if h.writeLogs != nil && len(entries) > 0 {
		h.writeLogs(entries)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Generic Webhook ───────────────────────────────────────────────────────────

// handleGeneric 处理 POST /api/v1/webhook/generic
// 请求体为任意 JSON；支持查询参数 metric_name 快速提取整个负载的数值
func (h *Handler) handleGeneric(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"仅支持 POST"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"error":"JSON 解析失败"}`, http.StatusBadRequest)
		return
	}

	now := time.Now().UnixMilli()
	var points []model.MetricPoint

	// 若指定了 metric_name 查询参数，尝试将整个 JSON 对象中的数值字段转换为指标
	if metricName := r.URL.Query().Get("metric_name"); metricName != "" {
		for k, v := range raw {
			if fv, ok := toFloat64(v); ok {
				points = append(points, model.MetricPoint{
					Name:      metricName,
					Labels:    map[string]string{"field": k},
					Value:     fv,
					Timestamp: now,
				})
			}
		}
	}

	if h.postMetrics != nil && len(points) > 0 {
		h.postMetrics(points)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]int{"extracted": len(points)})
}

// ── Configured Webhook ────────────────────────────────────────────────────────

// handleConfigured 根据 WebhookConfig 中的 ExtractRule 从 JSON 负载提取指标
func (h *Handler) handleConfigured(w http.ResponseWriter, r *http.Request, cfg WebhookConfig) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"仅支持 POST"}`, http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"error":"JSON 解析失败"}`, http.StatusBadRequest)
		return
	}

	now := time.Now().UnixMilli()
	var points []model.MetricPoint

	for _, rule := range cfg.Rules {
		v, err := extractJSONPath(raw, rule.JSONPath)
		if err != nil {
			continue
		}
		fv, ok := toFloat64(v)
		if !ok {
			continue
		}

		labels := map[string]string{"webhook": cfg.Name}
		for labelName, labelPath := range rule.LabelMappings {
			if lv, err := extractJSONPath(raw, labelPath); err == nil {
				labels[labelName] = fmt.Sprintf("%v", lv)
			}
		}

		points = append(points, model.MetricPoint{
			Name:      rule.MetricName,
			Labels:    labels,
			Value:     fv,
			Timestamp: now,
		})
	}

	if h.postMetrics != nil && len(points) > 0 {
		h.postMetrics(points)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]int{"extracted": len(points)})
}

// ── 工具函数 ──────────────────────────────────────────────────────────────────

// extractJSONPath 从 map 中按点分路径（如 "$.data.count"）提取值
func extractJSONPath(data map[string]interface{}, path string) (interface{}, error) {
	// 去掉前缀 "$." 或 "$"
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	if path == "" {
		return data, nil
	}

	parts := strings.SplitN(path, ".", 2)
	key := parts[0]
	val, ok := data[key]
	if !ok {
		return nil, fmt.Errorf("字段 %q 不存在", key)
	}
	if len(parts) == 1 {
		return val, nil
	}
	// 递归处理嵌套路径
	nested, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("字段 %q 不是对象", key)
	}
	return extractJSONPath(nested, parts[1])
}

// toFloat64 将 interface{} 转换为 float64（仅支持数值类型）
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// remarshal 将 map[string]interface{} 重新序列化并反序列化到目标结构体
func remarshal(src interface{}, dst interface{}) {
	b, _ := json.Marshal(src)
	json.Unmarshal(b, dst)
}
