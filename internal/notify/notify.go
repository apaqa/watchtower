// Package notify 实现多渠道通知系统：Webhook、Slack、Discord、Email、Console
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// ── 通知载荷 ──────────────────────────────────────────────────────────────────

// Notification 是传递给所有通知渠道的统一数据结构
type Notification struct {
	AlertName  string     // 告警规则名称
	Severity   string     // "critical" | "warning" | "info"
	Value      float64    // 触发时的指标值
	Message    string     // 人类可读描述
	State      string     // "firing" | "resolved"
	FiredAt    *time.Time // 触发时间（可选）
	ResolvedAt *time.Time // 恢复时间（可选）
}

// ── 接口定义 ──────────────────────────────────────────────────────────────────

// Channel 是所有通知后端需要实现的接口
type Channel interface {
	// ChannelType 返回渠道类型标识，如 "slack"、"webhook" 等
	ChannelType() string
	// Send 发送通知；若发送失败返回非 nil 错误
	Send(n Notification) error
}

// ── ConsoleChannel ─────────────────────────────────────────────────────────────

// ConsoleChannel 将通知打印到标准输出，适合开发和测试
type ConsoleChannel struct{}

// NewConsoleChannel 创建 ConsoleChannel 实例
func NewConsoleChannel() *ConsoleChannel { return &ConsoleChannel{} }

func (c *ConsoleChannel) ChannelType() string { return "console" }

func (c *ConsoleChannel) Send(n Notification) error {
	icon := alertIcon(n.State, n.Severity)
	fmt.Printf("[notify] %s [%s] %s — %s (value=%.4f)\n",
		icon, strings.ToUpper(n.Severity), n.AlertName, n.State, n.Value)
	return nil
}

// ── WebhookChannel ─────────────────────────────────────────────────────────────

// WebhookChannel 将 JSON 载荷 POST 到指定 URL
type WebhookChannel struct {
	URL    string
	client *http.Client
}

// NewWebhookChannel 创建 WebhookChannel 实例
func NewWebhookChannel(url string) *WebhookChannel {
	return &WebhookChannel{
		URL:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *WebhookChannel) ChannelType() string { return "webhook" }

// webhookPayload 是通用 Webhook 的 JSON 结构
type webhookPayload struct {
	AlertName  string     `json:"alert_name"`
	Severity   string     `json:"severity"`
	Value      float64    `json:"value"`
	Message    string     `json:"message"`
	State      string     `json:"state"`
	FiredAt    *time.Time `json:"fired_at,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func (c *WebhookChannel) Send(n Notification) error {
	payload := webhookPayload{
		AlertName:  n.AlertName,
		Severity:   n.Severity,
		Value:      n.Value,
		Message:    n.Message,
		State:      n.State,
		FiredAt:    n.FiredAt,
		ResolvedAt: n.ResolvedAt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook 序列化失败: %w", err)
	}
	resp, err := c.client.Post(c.URL, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("webhook POST 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook 返回错误状态码 %d", resp.StatusCode)
	}
	return nil
}

// ── SlackChannel ───────────────────────────────────────────────────────────────

// SlackChannel 通过 Incoming Webhook URL 向 Slack 发送消息
type SlackChannel struct {
	WebhookURL string
	client     *http.Client
}

// NewSlackChannel 创建 SlackChannel 实例
func NewSlackChannel(webhookURL string) *SlackChannel {
	return &SlackChannel{
		WebhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *SlackChannel) ChannelType() string { return "slack" }

func (c *SlackChannel) Send(n Notification) error {
	color := slackColor(n.State, n.Severity)
	title := fmt.Sprintf("%s Alert %s: %s", alertIcon(n.State, n.Severity), strings.Title(n.State), n.AlertName)
	payload := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color":     color,
				"title":     title,
				"text":      n.Message,
				"footer":    "WatchTower",
				"ts":        time.Now().Unix(),
				"mrkdwn_in": []string{"text"},
			},
		},
	}
	return c.postJSON(payload)
}

func (c *SlackChannel) postJSON(payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("Slack 消息序列化失败: %w", err)
	}
	resp, err := c.client.Post(c.WebhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("Slack POST 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Slack 返回错误状态码 %d", resp.StatusCode)
	}
	return nil
}

// slackColor 根据状态和严重级别返回 Slack 附件颜色
func slackColor(state, severity string) string {
	if state == "resolved" {
		return "#3fb950" // 绿色
	}
	switch severity {
	case "critical":
		return "#d73a49" // 红色
	case "warning":
		return "#d29922" // 黄色
	default:
		return "#58a6ff" // 蓝色（info）
	}
}

// ── DiscordChannel ─────────────────────────────────────────────────────────────

// DiscordChannel 通过 Discord Webhook URL 发送嵌入式消息
type DiscordChannel struct {
	WebhookURL string
	client     *http.Client
}

// NewDiscordChannel 创建 DiscordChannel 实例
func NewDiscordChannel(webhookURL string) *DiscordChannel {
	return &DiscordChannel{
		WebhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *DiscordChannel) ChannelType() string { return "discord" }

func (c *DiscordChannel) Send(n Notification) error {
	title := fmt.Sprintf("%s Alert %s: %s", alertIcon(n.State, n.Severity), strings.Title(n.State), n.AlertName)
	embed := map[string]interface{}{
		"title":       title,
		"description": n.Message,
		"color":       discordColor(n.State, n.Severity),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	payload := map[string]interface{}{
		"embeds": []interface{}{embed},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("Discord 消息序列化失败: %w", err)
	}
	resp, err := c.client.Post(c.WebhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("Discord POST 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Discord 返回错误状态码 %d", resp.StatusCode)
	}
	return nil
}

// discordColor 根据状态和严重级别返回 Discord 颜色整数（十进制）
func discordColor(state, severity string) int {
	if state == "resolved" {
		return 4153600 // 绿色 #3fb950
	}
	switch severity {
	case "critical":
		return 14090240 // 红色 #d73a00
	case "warning":
		return 13789954 // 黄色 #d29922
	default:
		return 5751827 // 蓝色 #58a6ff
	}
}

// ── EmailChannel ───────────────────────────────────────────────────────────────

// EmailChannel 通过 SMTP 发送告警邮件
type EmailChannel struct {
	Host     string   // SMTP 服务器主机名
	Port     int      // SMTP 端口，通常为 587（TLS）或 25
	From     string   // 发件人地址
	To       []string // 收件人地址列表
	Username string   // SMTP 认证用户名（可选）
	Password string   // SMTP 认证密码（可选）
}

// NewEmailChannel 创建 EmailChannel 实例
func NewEmailChannel(host string, port int, from string, to []string, username, password string) *EmailChannel {
	return &EmailChannel{
		Host: host, Port: port, From: from, To: to,
		Username: username, Password: password,
	}
}

func (c *EmailChannel) ChannelType() string { return "email" }

func (c *EmailChannel) Send(n Notification) error {
	subject := fmt.Sprintf("WatchTower: Alert %s — %s [%s]", strings.Title(n.State), n.AlertName, strings.ToUpper(n.Severity))
	body := fmt.Sprintf("Alert: %s\nSeverity: %s\nState: %s\nValue: %.4f\nMessage: %s\n",
		n.AlertName, n.Severity, n.State, n.Value, n.Message)

	header := fmt.Sprintf("To: %s\r\nFrom: %s\r\nSubject: %s\r\n\r\n",
		strings.Join(c.To, ", "), c.From, subject)
	msg := []byte(header + body + "\r\n")

	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)

	var auth smtp.Auth
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	}

	if err := smtp.SendMail(addr, auth, c.From, c.To, msg); err != nil {
		return fmt.Errorf("邮件发送失败: %w", err)
	}
	return nil
}

// ── NotificationHistory ────────────────────────────────────────────────────────

const maxHistorySize = 200

// NotificationRecord 记录一次通知发送的结果
type NotificationRecord struct {
	Timestamp   time.Time `json:"timestamp"`
	ChannelType string    `json:"channel_type"`
	AlertName   string    `json:"alert_name"`
	Severity    string    `json:"severity"`
	State       string    `json:"state"`
	Status      string    `json:"status"`         // "sent" | "failed"
	ErrorMsg    string    `json:"error,omitempty"` // 失败时的错误信息
}

// History 是通知记录的环形缓冲区（最多 200 条，最新在前）
type History struct {
	mu      sync.RWMutex
	records []NotificationRecord
}

// newHistory 创建空的通知历史存储
func newHistory() *History { return &History{} }

// Add 向历史记录头部插入新条目
func (h *History) Add(r NotificationRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append([]NotificationRecord{r}, h.records...)
	if len(h.records) > maxHistorySize {
		h.records = h.records[:maxHistorySize]
	}
}

// List 返回所有历史记录的副本（最新在前）
func (h *History) List() []NotificationRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]NotificationRecord, len(h.records))
	copy(out, h.records)
	return out
}

// Count 返回当前记录数
func (h *History) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.records)
}

// ── Router ────────────────────────────────────────────────────────────────────

// route 将一个渠道与可选的严重级别过滤器绑定
type route struct {
	channel    Channel
	severities map[string]bool // nil 或 empty 表示接收所有严重级别
}

func (r *route) matches(severity string) bool {
	if len(r.severities) == 0 {
		return true
	}
	return r.severities[severity]
}

// Router 将通知按严重级别分发到注册的渠道，并记录发送历史
type Router struct {
	mu      sync.RWMutex
	routes  []route
	history *History
}

// NewRouter 创建空的通知路由器
func NewRouter() *Router {
	return &Router{history: newHistory()}
}

// AddChannel 向路由器注册一个渠道；severities 为空时接收所有级别
func (r *Router) AddChannel(ch Channel, severities []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sev := make(map[string]bool, len(severities))
	for _, s := range severities {
		sev[s] = true
	}
	r.routes = append(r.routes, route{channel: ch, severities: sev})
}

// Dispatch 将通知分发给所有匹配严重级别的渠道（非阻塞）
func (r *Router) Dispatch(n Notification) {
	r.mu.RLock()
	routes := make([]route, len(r.routes))
	copy(routes, r.routes)
	r.mu.RUnlock()

	for _, rt := range routes {
		if !rt.matches(n.Severity) {
			continue
		}
		// 在独立 goroutine 中发送，避免阻塞评估循环
		ch := rt.channel
		h := r.history
		go func(ch Channel, n Notification) {
			err := ch.Send(n)
			rec := NotificationRecord{
				Timestamp:   time.Now(),
				ChannelType: ch.ChannelType(),
				AlertName:   n.AlertName,
				Severity:    n.Severity,
				State:       n.State,
				Status:      "sent",
			}
			if err != nil {
				rec.Status = "failed"
				rec.ErrorMsg = err.Error()
			}
			h.Add(rec)
		}(ch, n)
	}
}

// History 返回通知历史存储（用于 API 注册）
func (r *Router) History() *History { return r.history }

// ── 工具函数 ──────────────────────────────────────────────────────────────────

// alertIcon 根据状态和严重级别返回 emoji
func alertIcon(state, severity string) string {
	if state == "resolved" {
		return "✅"
	}
	switch severity {
	case "critical":
		return "🔥"
	case "warning":
		return "⚠️"
	default:
		return "ℹ️"
	}
}
