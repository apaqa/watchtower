// Package alert 实现告警规则定义、状态机和通知引擎
package alert

import (
	"fmt"
	"time"
)

// AlertState 描述一条告警规则的当前运行状态
type AlertState string

const (
	// StateInactive 表示条件当前为假，告警未激活
	StateInactive AlertState = "inactive"
	// StatePending 表示条件已变为真，但尚未持续足够长的时间
	StatePending AlertState = "pending"
	// StateFiring 表示条件持续为真且达到阈值时长，告警已触发
	StateFiring AlertState = "firing"
	// StateResolved 表示告警曾处于 Firing，条件已恢复为假
	StateResolved AlertState = "resolved"
)

// AlertRule 定义一条告警规则的全部参数
type AlertRule struct {
	// Name 是规则的唯一标识符
	Name string `json:"name"`
	// Expression 是用于求值的 WQL 表达式，应返回标量或布尔值
	Expression string `json:"wql_expression"`
	// Operator 是与阈值比较的运算符：>、<、>=、<=、==、!=
	Operator string `json:"operator"`
	// Threshold 是数值比较阈值
	Threshold float64 `json:"threshold"`
	// Duration 是条件必须持续为真才触发告警的时长字符串，如 "5m"、"1h"；"0" 或空字符串表示立即触发
	Duration string `json:"duration"`
	// Severity 是告警严重级别：critical / warning / info
	Severity string `json:"severity"`
	// WebhookURL 是可选的通知地址；为空则不发送 Webhook
	WebhookURL string `json:"webhook_url,omitempty"`
}

// Validate 检查告警规则字段的合法性
func (r *AlertRule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("告警规则名称不能为空")
	}
	if r.Expression == "" {
		return fmt.Errorf("WQL 表达式不能为空")
	}
	switch r.Operator {
	case ">", "<", ">=", "<=", "==", "!=":
	default:
		return fmt.Errorf("不支持的运算符 %q（支持 >, <, >=, <=, ==, !=）", r.Operator)
	}
	switch r.Severity {
	case "critical", "warning", "info":
	default:
		return fmt.Errorf("不支持的严重级别 %q（支持 critical/warning/info）", r.Severity)
	}
	return nil
}

// ParseDuration 将 Duration 字段解析为 time.Duration；无效或空值返回 0
func (r *AlertRule) ParseDuration() time.Duration {
	if r.Duration == "" || r.Duration == "0" {
		return 0
	}
	d, err := time.ParseDuration(r.Duration)
	if err != nil {
		return 0
	}
	return d
}

// Alert 记录一条规则的完整运行状态
type Alert struct {
	// Rule 是告警对应的原始规则
	Rule AlertRule `json:"rule"`
	// State 是当前状态
	State AlertState `json:"state"`
	// PendingAt 是进入 Pending 状态的时间
	PendingAt *time.Time `json:"pending_at,omitempty"`
	// FiredAt 是进入 Firing 状态的时间
	FiredAt *time.Time `json:"fired_at,omitempty"`
	// ResolvedAt 是进入 Resolved 状态的时间
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// LastValue 是最近一次 WQL 求值得到的数值
	LastValue float64 `json:"last_value"`
	// Message 是人类可读的描述
	Message string `json:"message"`
}

// AlertEvent 是一次告警状态变化的历史记录
type AlertEvent struct {
	// RuleName 关联的规则名称
	RuleName string `json:"rule_name"`
	// State 变化后的新状态
	State AlertState `json:"state"`
	// Value 状态变化时的指标值
	Value float64 `json:"value"`
	// Message 人类可读描述
	Message string `json:"message"`
	// Timestamp 状态变化发生时间
	Timestamp time.Time `json:"timestamp"`
}

// WebhookPayload 是发送给外部 Webhook 端点的 JSON 载荷结构
type WebhookPayload struct {
	AlertName  string     `json:"alert_name"`
	Severity   string     `json:"severity"`
	Value      float64    `json:"value"`
	Message    string     `json:"message"`
	State      AlertState `json:"state"`
	FiredAt    *time.Time `json:"fired_at,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}
