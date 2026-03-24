package incident

import (
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 创建事故测试 ───────────────────────────────────────────────────────────────

func TestIncident_Create_Success(t *testing.T) {
	s := New()
	inc, err := s.Create("API 服务中断", SeverityCritical, []string{"http_error_rate"}, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inc.Title != "API 服务中断" {
		t.Errorf("expected title 'API 服务中断', got %q", inc.Title)
	}
	if inc.Severity != SeverityCritical {
		t.Errorf("expected severity critical, got %s", inc.Severity)
	}
	if inc.Status != StatusInvestigating {
		t.Errorf("expected status investigating, got %s", inc.Status)
	}
	if len(inc.Timeline) == 0 {
		t.Error("expected non-empty timeline on creation")
	}
	if inc.Timeline[0].Type != EventCreated {
		t.Errorf("expected first timeline event type=created, got %s", inc.Timeline[0].Type)
	}
	if inc.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestIncident_Create_InvalidTitle(t *testing.T) {
	s := New()
	_, err := s.Create("", SeverityMajor, nil, "api")
	if err == nil {
		t.Error("expected error for empty title")
	}
}

func TestIncident_Create_InvalidSeverity(t *testing.T) {
	s := New()
	_, err := s.Create("Test", "unknown", nil, "api")
	if err == nil {
		t.Error("expected error for unknown severity")
	}
}

// ── 状态更新测试 ───────────────────────────────────────────────────────────────

func TestIncident_UpdateStatus(t *testing.T) {
	s := New()
	inc, _ := s.Create("DB 连接超时", SeverityMajor, nil, "bob")

	updated, err := s.Update(inc.ID, StatusIdentified, "", "", "bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != StatusIdentified {
		t.Errorf("expected status identified, got %s", updated.Status)
	}
	// 时间线应增加 updated 事件
	hasUpdated := false
	for _, ev := range updated.Timeline {
		if ev.Type == EventUpdated {
			hasUpdated = true
			break
		}
	}
	if !hasUpdated {
		t.Error("expected EventUpdated in timeline after status change")
	}
}

func TestIncident_UpdateAssignee(t *testing.T) {
	s := New()
	inc, _ := s.Create("网络抖动", SeverityMinor, nil, "api")

	updated, err := s.Update(inc.ID, "", "", "charlie", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.AssignedTo != "charlie" {
		t.Errorf("expected assigned_to=charlie, got %q", updated.AssignedTo)
	}
	// 时间线应包含 assigned 事件
	hasAssigned := false
	for _, ev := range updated.Timeline {
		if ev.Type == EventAssigned && ev.Author == "api" {
			hasAssigned = true
		}
	}
	if !hasAssigned {
		t.Error("expected EventAssigned in timeline after assignee change")
	}
}

// ── 评论测试 ───────────────────────────────────────────────────────────────────

func TestIncident_AddComment(t *testing.T) {
	s := New()
	inc, _ := s.Create("磁盘空间告警", SeverityMajor, nil, "api")

	commented, err := s.AddComment(inc.ID, "alice", "已确认是日志文件堆积，正在清理")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	last := commented.Timeline[len(commented.Timeline)-1]
	if last.Type != EventComment {
		t.Errorf("expected last timeline event type=comment, got %s", last.Type)
	}
	if last.Author != "alice" {
		t.Errorf("expected author=alice, got %q", last.Author)
	}
	if last.Message != "已确认是日志文件堆积，正在清理" {
		t.Errorf("unexpected message: %q", last.Message)
	}
}

func TestIncident_AddComment_EmptyMessage(t *testing.T) {
	s := New()
	inc, _ := s.Create("Test", SeverityMinor, nil, "api")
	_, err := s.AddComment(inc.ID, "api", "")
	if err == nil {
		t.Error("expected error for empty comment message")
	}
}

// ── 解决事故测试 ───────────────────────────────────────────────────────────────

func TestIncident_Resolve(t *testing.T) {
	s := New()
	inc, _ := s.Create("CPU 过载", SeverityCritical, []string{"cpu_usage_percent"}, "api")

	resolved, err := s.Resolve(inc.ID, "ops-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Status != StatusResolved {
		t.Errorf("expected status resolved, got %s", resolved.Status)
	}
	if resolved.ResolvedAt == nil {
		t.Error("expected non-nil resolved_at")
	}
	last := resolved.Timeline[len(resolved.Timeline)-1]
	if last.Type != EventResolved {
		t.Errorf("expected last timeline event type=resolved, got %s", last.Type)
	}
}

func TestIncident_Resolve_NotFound(t *testing.T) {
	s := New()
	_, err := s.Resolve("nonexistent", "api")
	if err == nil {
		t.Error("expected error for nonexistent incident")
	}
}

// ── 时间线顺序测试 ─────────────────────────────────────────────────────────────

func TestIncident_TimelineOrdering(t *testing.T) {
	s := New()
	inc, _ := s.Create("服务降级", SeverityMajor, nil, "api")

	s.Update(inc.ID, StatusIdentified, "", "", "alice")
	s.AddComment(inc.ID, "bob", "正在调查根本原因")
	s.Update(inc.ID, StatusMonitoring, "", "", "alice")

	final, _ := s.Get(inc.ID)
	// 验证时间线按时间戳递增排列
	for i := 1; i < len(final.Timeline); i++ {
		if final.Timeline[i].Timestamp.Before(final.Timeline[i-1].Timestamp) {
			t.Errorf("timeline out of order at index %d: %v < %v",
				i, final.Timeline[i].Timestamp, final.Timeline[i-1].Timestamp)
		}
	}
	// 验证第一条是 created，最后一条是 updated
	if final.Timeline[0].Type != EventCreated {
		t.Errorf("expected first event to be 'created', got %s", final.Timeline[0].Type)
	}
}

// ── 按状态过滤测试 ─────────────────────────────────────────────────────────────

func TestIncident_FilterByStatus(t *testing.T) {
	s := New()
	s.Create("事故 A", SeverityCritical, nil, "api")
	inc2, _ := s.Create("事故 B", SeverityMajor, nil, "api")
	s.Resolve(inc2.ID, "api")

	investigating := s.List(StatusInvestigating, "")
	if len(investigating) != 1 {
		t.Errorf("expected 1 investigating incident, got %d", len(investigating))
	}
	resolved := s.List(StatusResolved, "")
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved incident, got %d", len(resolved))
	}
	all := s.List("", "")
	if len(all) != 2 {
		t.Errorf("expected 2 total incidents, got %d", len(all))
	}
}

// ── 按严重级别过滤测试 ─────────────────────────────────────────────────────────

func TestIncident_FilterBySeverity(t *testing.T) {
	s := New()
	s.Create("Critical 事故", SeverityCritical, nil, "api")
	s.Create("Major 事故", SeverityMajor, nil, "api")
	s.Create("Minor 事故", SeverityMinor, nil, "api")

	criticals := s.List("", SeverityCritical)
	if len(criticals) != 1 {
		t.Errorf("expected 1 critical, got %d", len(criticals))
	}
	minors := s.List("", SeverityMinor)
	if len(minors) != 1 {
		t.Errorf("expected 1 minor, got %d", len(minors))
	}
}

// ── 自动创建事故测试（基于持续触发告警）────────────────────────────────────────

func TestIncident_AutoCreateFromAlert(t *testing.T) {
	db := tsdb.New()
	db.Write([]model.MetricPoint{{
		Name:      "cpu_usage_percent",
		Labels:    map[string]string{},
		Value:     95.0,
		Timestamp: time.Now().UnixMilli(),
	}})

	alertEng := alert.NewEngine(db)
	alertEng.AddRule(alert.AlertRule{
		Name:       "high_cpu",
		Expression: "cpu_usage_percent",
		Operator:   ">",
		Threshold:  90.0,
		Severity:   "critical",
	})
	alertEng.Evaluate() // 设置 FiredAt = time.Now()

	s := New()
	s.SetAlertEngine(alertEng)

	// 模拟 6 分钟后检查（超过 5 分钟触发阈值）
	s.checkAlerts(time.Now().Add(6 * time.Minute))

	incidents := s.List("", "")
	if len(incidents) == 0 {
		t.Fatal("expected at least one auto-created incident")
	}
	if incidents[0].Severity != SeverityCritical {
		t.Errorf("expected critical severity, got %s", incidents[0].Severity)
	}
	if len(incidents[0].RelatedAlerts) == 0 || incidents[0].RelatedAlerts[0] != "high_cpu" {
		t.Errorf("expected related_alerts=[high_cpu], got %v", incidents[0].RelatedAlerts)
	}
}

func TestIncident_AutoCreate_NoDoubleCreate(t *testing.T) {
	db := tsdb.New()
	db.Write([]model.MetricPoint{{
		Name:      "cpu_usage_percent",
		Labels:    map[string]string{},
		Value:     95.0,
		Timestamp: time.Now().UnixMilli(),
	}})

	alertEng := alert.NewEngine(db)
	alertEng.AddRule(alert.AlertRule{
		Name:      "high_cpu2",
		Expression: "cpu_usage_percent",
		Operator:  ">",
		Threshold: 90.0,
		Severity:  "warning",
	})
	alertEng.Evaluate()

	s := New()
	s.SetAlertEngine(alertEng)

	// 调用两次，应只创建一个事故
	s.checkAlerts(time.Now().Add(6 * time.Minute))
	s.checkAlerts(time.Now().Add(7 * time.Minute))

	incidents := s.List("", "")
	if len(incidents) != 1 {
		t.Errorf("expected exactly 1 incident (no duplicate), got %d", len(incidents))
	}
}
