// Package incident 提供事故管理功能：创建、更新、评论和解决事故，
// 以及基于持续触发告警的自动创建机制。
package incident

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/notify"
)

// EventType 事故时间线事件类型
type EventType string

const (
	EventCreated  EventType = "created"
	EventUpdated  EventType = "updated"
	EventAssigned EventType = "assigned"
	EventResolved EventType = "resolved"
	EventComment  EventType = "comment"
)

// Severity 事故严重级别
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityMajor    Severity = "major"
	SeverityMinor    Severity = "minor"
)

// Status 事故处理状态
type Status string

const (
	StatusInvestigating Status = "investigating"
	StatusIdentified    Status = "identified"
	StatusMonitoring    Status = "monitoring"
	StatusResolved      Status = "resolved"
)

// IncidentEvent 是事故时间线中的单条记录
type IncidentEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Author    string    `json:"author"`
	Message   string    `json:"message"`
	Type      EventType `json:"type"`
}

// Incident 表示一个服务中断事故
type Incident struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	Severity      Severity        `json:"severity"`
	Status        Status          `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	ResolvedAt    *time.Time      `json:"resolved_at,omitempty"`
	AssignedTo    string          `json:"assigned_to,omitempty"`
	RelatedAlerts []string        `json:"related_alerts,omitempty"`
	Timeline      []IncidentEvent `json:"timeline"`
}

// Store 事故存储（线程安全）
type Store struct {
	mu           sync.RWMutex
	incidents    map[string]*Incident

	alertMu      sync.Mutex
	alertedSet   map[string]string // 告警名称 → 事故 ID（避免重复自动创建）

	alertEngine  *alert.Engine  // 可选，用于自动创建事故
	oncallFn     func() string  // 可选，返回当前值班人姓名
	notifyRouter *notify.Router // 可选，创建事故后通知值班人

	stopCh  chan struct{}
	started bool
}

// New 创建事故存储实例
func New() *Store {
	return &Store{
		incidents:  make(map[string]*Incident),
		alertedSet: make(map[string]string),
		stopCh:     make(chan struct{}),
	}
}

// SetAlertEngine 注入告警引擎，用于自动创建事故（必须在 Start 之前调用）
func (s *Store) SetAlertEngine(eng *alert.Engine) { s.alertEngine = eng }

// SetOnCallFn 注入值班查询函数（必须在 Start 之前调用）
func (s *Store) SetOnCallFn(fn func() string) { s.oncallFn = fn }

// SetNotifyRouter 注入通知路由器（必须在 Start 之前调用）
func (s *Store) SetNotifyRouter(r *notify.Router) { s.notifyRouter = r }

// Start 启动后台告警监视协程（幂等）
func (s *Store) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.watchAlerts()
}

// Stop 停止后台协程
func (s *Store) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// watchAlerts 每分钟检查一次告警引擎，自动创建事故
func (s *Store) watchAlerts() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.checkAlerts(time.Now())
		case <-s.stopCh:
			return
		}
	}
}

// checkAlerts 检查持续触发超过 5 分钟的告警，自动创建事故。
// now 参数由调用方注入，便于测试时模拟时间流逝。
func (s *Store) checkAlerts(now time.Time) {
	if s.alertEngine == nil {
		return
	}
	alerts := s.alertEngine.GetAlerts()
	activeNames := make(map[string]struct{}, len(alerts))

	for _, a := range alerts {
		if a.State != alert.StateFiring || a.FiredAt == nil {
			continue
		}
		// 仅对持续触发超过 5 分钟的告警自动创建事故
		if now.Sub(*a.FiredAt) < 5*time.Minute {
			continue
		}
		activeNames[a.Rule.Name] = struct{}{}

		s.alertMu.Lock()
		_, exists := s.alertedSet[a.Rule.Name]
		s.alertMu.Unlock()
		if exists {
			continue
		}

		inc, _ := s.doCreate(
			fmt.Sprintf("告警触发：%s", a.Rule.Name),
			mapAlertSeverity(a.Rule.Severity),
			[]string{a.Rule.Name},
			"system",
		)

		s.alertMu.Lock()
		s.alertedSet[a.Rule.Name] = inc.ID
		s.alertMu.Unlock()
	}

	// 清理已停止触发的告警条目，允许下次触发时重新创建事故
	s.alertMu.Lock()
	for name := range s.alertedSet {
		if _, active := activeNames[name]; !active {
			delete(s.alertedSet, name)
		}
	}
	s.alertMu.Unlock()
}

// mapAlertSeverity 将告警严重级别映射为事故严重级别
func mapAlertSeverity(sev string) Severity {
	switch sev {
	case "critical":
		return SeverityCritical
	case "info":
		return SeverityMinor
	default:
		return SeverityMajor
	}
}

// nextIDCounter 用于生成递增的事故 ID
var nextIDCounter int64

// generateID 生成唯一事故 ID
func generateID() string {
	nextIDCounter++
	return fmt.Sprintf("inc-%d-%d", time.Now().UnixNano()/1e6, nextIDCounter)
}

// doCreate 内部创建事故（不持有任何锁，可安全地从任何上下文调用）
func (s *Store) doCreate(title string, severity Severity, relatedAlerts []string, author string) (*Incident, error) {
	now := time.Now()

	// 查询当前值班人
	assignee := ""
	if s.oncallFn != nil {
		assignee = s.oncallFn()
	}

	inc := &Incident{
		ID:            generateID(),
		Title:         title,
		Severity:      severity,
		Status:        StatusInvestigating,
		CreatedAt:     now,
		UpdatedAt:     now,
		AssignedTo:    assignee,
		RelatedAlerts: relatedAlerts,
		Timeline: []IncidentEvent{{
			Timestamp: now,
			Author:    author,
			Message:   fmt.Sprintf("事故已创建：%s", title),
			Type:      EventCreated,
		}},
	}
	if assignee != "" {
		inc.Timeline = append(inc.Timeline, IncidentEvent{
			Timestamp: now,
			Author:    "system",
			Message:   fmt.Sprintf("已自动分配给值班人员：%s", assignee),
			Type:      EventAssigned,
		})
	}

	s.mu.Lock()
	s.incidents[inc.ID] = inc
	s.mu.Unlock()

	// 在锁外通知值班人员
	if assignee != "" && s.notifyRouter != nil {
		s.notifyRouter.Dispatch(notify.Notification{
			AlertName: inc.Title,
			Severity:  string(inc.Severity),
			Message:   fmt.Sprintf("新事故已分配给您：%s [%s]", inc.Title, inc.Severity),
			State:     "incident",
		})
	}
	return inc, nil
}

// Create 创建新事故（含输入校验）
func (s *Store) Create(title string, severity Severity, relatedAlerts []string, author string) (*Incident, error) {
	if title == "" {
		return nil, fmt.Errorf("事故标题不能为空")
	}
	if severity != SeverityCritical && severity != SeverityMajor && severity != SeverityMinor {
		return nil, fmt.Errorf("无效的严重级别：%s", severity)
	}
	return s.doCreate(title, severity, relatedAlerts, author)
}

// Get 返回指定 ID 的事故深度副本；若不存在则返回 false
func (s *Store) Get(id string) (*Incident, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inc, ok := s.incidents[id]
	if !ok {
		return nil, false
	}
	return copyIncident(inc), true
}

// List 返回所有事故（按创建时间降序）；statusFilter 和 severityFilter 为空字符串时不过滤
func (s *Store) List(statusFilter Status, severityFilter Severity) []*Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Incident, 0, len(s.incidents))
	for _, inc := range s.incidents {
		if statusFilter != "" && inc.Status != statusFilter {
			continue
		}
		if severityFilter != "" && inc.Severity != severityFilter {
			continue
		}
		result = append(result, copyIncident(inc))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

// Update 更新事故的状态、严重级别或负责人（传入空字符串表示不修改该字段）
func (s *Store) Update(id string, newStatus Status, newSeverity Severity, assignedTo string, author string) (*Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.incidents[id]
	if !ok {
		return nil, fmt.Errorf("事故 %s 不存在", id)
	}
	now := time.Now()
	var changes []string

	if newStatus != "" && newStatus != inc.Status {
		changes = append(changes, fmt.Sprintf("状态：%s → %s", inc.Status, newStatus))
		inc.Status = newStatus
	}
	if newSeverity != "" && newSeverity != inc.Severity {
		changes = append(changes, fmt.Sprintf("级别：%s → %s", inc.Severity, newSeverity))
		inc.Severity = newSeverity
	}
	if assignedTo != "" && assignedTo != inc.AssignedTo {
		changes = append(changes, fmt.Sprintf("负责人：%s → %s", inc.AssignedTo, assignedTo))
		inc.AssignedTo = assignedTo
		inc.Timeline = append(inc.Timeline, IncidentEvent{
			Timestamp: now,
			Author:    author,
			Message:   fmt.Sprintf("已分配给：%s", assignedTo),
			Type:      EventAssigned,
		})
	}
	if len(changes) > 0 {
		inc.UpdatedAt = now
		inc.Timeline = append(inc.Timeline, IncidentEvent{
			Timestamp: now,
			Author:    author,
			Message:   strings.Join(changes, "；"),
			Type:      EventUpdated,
		})
	}
	return copyIncident(inc), nil
}

// AddComment 向事故时间线追加一条评论
func (s *Store) AddComment(id, author, message string) (*Incident, error) {
	if message == "" {
		return nil, fmt.Errorf("评论内容不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.incidents[id]
	if !ok {
		return nil, fmt.Errorf("事故 %s 不存在", id)
	}
	now := time.Now()
	inc.UpdatedAt = now
	inc.Timeline = append(inc.Timeline, IncidentEvent{
		Timestamp: now,
		Author:    author,
		Message:   message,
		Type:      EventComment,
	})
	return copyIncident(inc), nil
}

// Resolve 将事故标记为已解决并记录解决时间
func (s *Store) Resolve(id, author string) (*Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.incidents[id]
	if !ok {
		return nil, fmt.Errorf("事故 %s 不存在", id)
	}
	now := time.Now()
	inc.Status = StatusResolved
	inc.UpdatedAt = now
	inc.ResolvedAt = &now
	inc.Timeline = append(inc.Timeline, IncidentEvent{
		Timestamp: now,
		Author:    author,
		Message:   "事故已解决",
		Type:      EventResolved,
	})
	return copyIncident(inc), nil
}

// copyIncident 创建事故深度副本，避免外部修改影响存储中的原始数据
func copyIncident(inc *Incident) *Incident {
	cp := *inc
	cp.Timeline = make([]IncidentEvent, len(inc.Timeline))
	copy(cp.Timeline, inc.Timeline)
	if inc.RelatedAlerts != nil {
		cp.RelatedAlerts = make([]string, len(inc.RelatedAlerts))
		copy(cp.RelatedAlerts, inc.RelatedAlerts)
	}
	if inc.ResolvedAt != nil {
		t := *inc.ResolvedAt
		cp.ResolvedAt = &t
	}
	return &cp
}
