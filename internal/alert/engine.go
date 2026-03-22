// Package alert - 告警引擎：周期性求值规则、驱动状态机、发送 Webhook 通知
package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/apaqa/watchtower/internal/wql"
)

const (
	// evalInterval 是告警规则的评估间隔
	evalInterval = 15 * time.Second
	// maxHistory 是保留的最大历史记录条数
	maxHistory = 100
)

// Engine 是告警引擎，负责周期性求值规则、维护状态机并发送通知
type Engine struct {
	mu      sync.RWMutex
	db      *tsdb.TSDB
	rules   map[string]*AlertRule // 规则名称 → 规则
	alerts  map[string]*Alert     // 规则名称 → 当前告警状态
	history []*AlertEvent         // 最近 maxHistory 条状态变化记录（最新在前）
	stopCh  chan struct{}
	client  *http.Client // 用于发送 Webhook 请求
}

// NewEngine 创建告警引擎实例
func NewEngine(db *tsdb.TSDB) *Engine {
	return &Engine{
		db:     db,
		rules:  make(map[string]*AlertRule),
		alerts: make(map[string]*Alert),
		stopCh: make(chan struct{}),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// AddRule 添加或替换一条告警规则；规则名称相同时覆盖原有规则
func (e *Engine) AddRule(rule AlertRule) error {
	if err := rule.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules[rule.Name] = &rule
	// 若该规则尚无告警状态，初始化为 Inactive
	if _, ok := e.alerts[rule.Name]; !ok {
		e.alerts[rule.Name] = &Alert{Rule: rule, State: StateInactive}
	} else {
		e.alerts[rule.Name].Rule = rule
	}
	return nil
}

// RemoveRule 删除指定名称的告警规则及其运行状态
func (e *Engine) RemoveRule(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.rules[name]; !ok {
		return fmt.Errorf("告警规则 %q 不存在", name)
	}
	delete(e.rules, name)
	delete(e.alerts, name)
	return nil
}

// GetAlerts 返回所有规则的当前告警状态副本
func (e *Engine) GetAlerts() []Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]Alert, 0, len(e.alerts))
	for _, a := range e.alerts {
		result = append(result, *a)
	}
	return result
}

// GetActiveAlerts 返回当前处于 Firing 状态的告警列表
func (e *Engine) GetActiveAlerts() []Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var result []Alert
	for _, a := range e.alerts {
		if a.State == StateFiring {
			result = append(result, *a)
		}
	}
	return result
}

// GetHistory 返回最近 maxHistory 条告警状态变化记录（最新在前）
func (e *Engine) GetHistory() []AlertEvent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]AlertEvent, len(e.history))
	for i, ev := range e.history {
		result[i] = *ev
	}
	return result
}

// Start 在后台 goroutine 中启动评估循环
func (e *Engine) Start() {
	go e.evalLoop()
}

// Stop 停止评估循环
func (e *Engine) Stop() {
	close(e.stopCh)
}

// Evaluate 立即对所有规则执行一轮评估（主要用于测试）
func (e *Engine) Evaluate() {
	e.evaluate()
}

// evalLoop 定时触发评估循环
func (e *Engine) evalLoop() {
	ticker := time.NewTicker(evalInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.evaluate()
		case <-e.stopCh:
			return
		}
	}
}

// evaluate 对所有规则执行一轮求值
func (e *Engine) evaluate() {
	// 先在读锁下复制规则列表，避免在求值期间长时间持有写锁
	e.mu.RLock()
	rules := make([]*AlertRule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, r)
	}
	e.mu.RUnlock()

	for _, rule := range rules {
		e.evaluateRule(rule)
	}
}

// evaluateRule 对单条规则求值并驱动状态机
//
// 状态转换图：
//   Inactive → (条件为真)     → Pending
//   Pending  → (条件持续为真且达到 duration) → Firing → 发送 Firing Webhook
//   Pending  → (条件变假)     → Inactive
//   Firing   → (条件变假)     → Resolved → 发送 Resolved Webhook
//   Resolved → (下一轮评估)   → Inactive
func (e *Engine) evaluateRule(rule *AlertRule) {
	now := time.Now()

	// 第一步：在锁外执行 WQL 求值（可能较慢）
	result, evalErr := wql.EvalQuery(e.db, rule.Expression)

	var conditionTrue bool
	var currentValue float64

	if evalErr == nil {
		switch result.Type {
		case wql.ResultScalar:
			if result.Scalar != nil {
				currentValue = *result.Scalar
				conditionTrue = applyOperator(rule.Operator, currentValue, rule.Threshold)
			}
		case wql.ResultVector:
			// 向量结果：对所有样本求平均后比较
			if len(result.Vector) > 0 {
				sum := 0.0
				for _, s := range result.Vector {
					sum += s.Value
				}
				currentValue = sum / float64(len(result.Vector))
				conditionTrue = applyOperator(rule.Operator, currentValue, rule.Threshold)
			}
		case wql.ResultBool:
			if result.Bool != nil && *result.Bool {
				conditionTrue = true
				currentValue = 1
			}
		}
	}
	// evalErr 不为 nil（如指标尚无数据）时，conditionTrue = false，视为条件不满足

	message := fmt.Sprintf("%s 当前值 %.4f（阈值：%s %.4f）",
		rule.Expression, currentValue, rule.Operator, rule.Threshold)

	// 第二步：在写锁下更新状态机
	e.mu.Lock()

	alert, ok := e.alerts[rule.Name]
	if !ok {
		// 规则被并发删除，直接跳过
		e.mu.Unlock()
		return
	}

	alert.LastValue = currentValue
	alert.Message = message
	prevState := alert.State

	// 需要在锁外发送的 Webhook 载荷（nil 表示不需要发送）
	var webhookPayload *WebhookPayload

	switch alert.State {
	case StateInactive:
		if conditionTrue {
			dur := rule.ParseDuration()
			if dur == 0 {
				// duration=0：条件满足时立即触发，跳过 Pending 状态
				alert.State = StateFiring
				t := now
				alert.FiredAt = &t
				alert.ResolvedAt = nil
				webhookPayload = &WebhookPayload{
					AlertName: rule.Name,
					Severity:  rule.Severity,
					Value:     currentValue,
					Message:   message,
					State:     StateFiring,
					FiredAt:   &t,
				}
			} else {
				alert.State = StatePending
				t := now
				alert.PendingAt = &t
			}
		}

	case StatePending:
		if !conditionTrue {
			// 条件变假：回到 Inactive，重置计时
			alert.State = StateInactive
			alert.PendingAt = nil
		} else {
			// 条件为真：检查是否达到 duration
			dur := rule.ParseDuration()
			if dur == 0 || now.Sub(*alert.PendingAt) >= dur {
				alert.State = StateFiring
				t := now
				alert.FiredAt = &t
				alert.ResolvedAt = nil
				webhookPayload = &WebhookPayload{
					AlertName: rule.Name,
					Severity:  rule.Severity,
					Value:     currentValue,
					Message:   message,
					State:     StateFiring,
					FiredAt:   &t,
				}
			}
		}

	case StateFiring:
		if !conditionTrue {
			alert.State = StateResolved
			t := now
			alert.ResolvedAt = &t
			webhookPayload = &WebhookPayload{
				AlertName:  rule.Name,
				Severity:   rule.Severity,
				Value:      currentValue,
				Message:    message,
				State:      StateResolved,
				FiredAt:    alert.FiredAt,
				ResolvedAt: &t,
			}
		}

	case StateResolved:
		// Resolved 是瞬态状态，下一轮评估重置为 Inactive
		alert.State = StateInactive
		alert.PendingAt = nil
		alert.FiredAt = nil
		alert.ResolvedAt = nil
	}

	// 记录状态变化历史
	if alert.State != prevState {
		e.addHistoryLocked(&AlertEvent{
			RuleName:  rule.Name,
			State:     alert.State,
			Value:     currentValue,
			Message:   message,
			Timestamp: now,
		})
	}

	webhookURL := rule.WebhookURL
	e.mu.Unlock()

	// 第三步：在锁外发送 Webhook（避免在持锁时执行 I/O）
	if webhookPayload != nil && webhookURL != "" {
		go e.postWebhook(webhookURL, *webhookPayload)
	}
}

// addHistoryLocked 在历史队列头部插入新记录（调用方必须持有写锁）
func (e *Engine) addHistoryLocked(ev *AlertEvent) {
	e.history = append([]*AlertEvent{ev}, e.history...)
	if len(e.history) > maxHistory {
		e.history = e.history[:maxHistory]
	}
}

// postWebhook 将 JSON 载荷以 POST 方式发送到指定 URL（在独立 goroutine 中调用）
func (e *Engine) postWebhook(url string, payload WebhookPayload) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	resp, err := e.client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return
	}
	resp.Body.Close()
}

// applyOperator 将比较运算符应用于两个浮点数
func applyOperator(op string, a, b float64) bool {
	switch op {
	case ">":
		return a > b
	case "<":
		return a < b
	case ">=":
		return a >= b
	case "<=":
		return a <= b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}
