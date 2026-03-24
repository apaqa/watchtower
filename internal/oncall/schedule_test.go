package oncall

import (
	"testing"
	"time"
)

// allDays 包含一周所有星期的缩写
var allDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// ── 基础 Schedule 测试 ─────────────────────────────────────────────────────────

func TestOncall_EmptySchedule(t *testing.T) {
	s := New()
	info := s.CurrentOnCall()
	if info.HasOnCall {
		t.Error("expected no on-call for empty scheduler")
	}
	if info.User != "" {
		t.Errorf("expected empty user, got %q", info.User)
	}
}

func TestOncall_SetAndGetSchedule(t *testing.T) {
	s := New()
	sc := &OnCallSchedule{
		Name:     "default",
		Timezone: "UTC",
		Rotation: []OnCallSlot{
			{UserName: "alice", StartHour: 9, EndHour: 17, Days: []string{"mon", "tue", "wed", "thu", "fri"}},
			{UserName: "bob", StartHour: 17, EndHour: 24, Days: allDays},
		},
	}
	if err := s.SetSchedule(sc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := s.GetSchedule()
	if got == nil {
		t.Fatal("expected non-nil schedule")
	}
	if got.Name != "default" {
		t.Errorf("expected name=default, got %q", got.Name)
	}
	if len(got.Rotation) != 2 {
		t.Errorf("expected 2 rotation slots, got %d", len(got.Rotation))
	}
}

func TestOncall_SetSchedule_Nil(t *testing.T) {
	s := New()
	if err := s.SetSchedule(nil); err == nil {
		t.Error("expected error for nil schedule")
	}
}

func TestOncall_SetSchedule_InvalidHour(t *testing.T) {
	s := New()
	sc := &OnCallSchedule{
		Rotation: []OnCallSlot{
			{UserName: "alice", StartHour: 25, EndHour: 30, Days: allDays},
		},
	}
	if err := s.SetSchedule(sc); err == nil {
		t.Error("expected error for invalid hour range")
	}
}

func TestOncall_SetSchedule_StartAfterEnd(t *testing.T) {
	s := New()
	sc := &OnCallSchedule{
		Rotation: []OnCallSlot{
			{UserName: "alice", StartHour: 17, EndHour: 9, Days: allDays},
		},
	}
	if err := s.SetSchedule(sc); err == nil {
		t.Error("expected error when start_hour >= end_hour")
	}
}

// ── 当前值班查询测试 ───────────────────────────────────────────────────────────

func TestOncall_CurrentOnCall_AllDayAllHours(t *testing.T) {
	s := New()
	// 覆盖全天全周的排班
	sc := &OnCallSchedule{
		Name:     "fullcover",
		Timezone: "UTC",
		Rotation: []OnCallSlot{
			{UserName: "alice", StartHour: 0, EndHour: 24, Days: allDays},
		},
	}
	s.SetSchedule(sc)
	info := s.CurrentOnCall()
	if !info.HasOnCall {
		t.Error("expected on-call when schedule covers all hours")
	}
	if info.User != "alice" {
		t.Errorf("expected user=alice, got %q", info.User)
	}
	if info.Until == 0 {
		t.Error("expected non-zero until_ms")
	}
}

func TestOncall_CurrentOnCall_NoMatchingHour(t *testing.T) {
	s := New()
	// 强制设置一个过去时段（比如凌晨 2-3 点），当前通常不匹配
	// 用一个极窄时间窗来确保当前不在其中
	now := time.Now().UTC()
	hour := now.Hour()
	// 设置当前小时之前的时段
	startH := (hour + 1) % 24
	endH := startH + 1
	if endH > 24 {
		endH = 24
	}
	sc := &OnCallSchedule{
		Name:     "narrow",
		Timezone: "UTC",
		Rotation: []OnCallSlot{
			{UserName: "bob", StartHour: startH, EndHour: endH, Days: allDays},
		},
	}
	s.SetSchedule(sc)
	info := s.CurrentOnCall()
	if info.HasOnCall {
		t.Errorf("expected no on-call for narrow future slot (hour=%d, slot=%d-%d)",
			hour, startH, endH)
	}
}

func TestOncall_CurrentOnCallName(t *testing.T) {
	s := New()
	sc := &OnCallSchedule{
		Name:     "test",
		Timezone: "UTC",
		Rotation: []OnCallSlot{
			{UserName: "carol", StartHour: 0, EndHour: 24, Days: allDays},
		},
	}
	s.SetSchedule(sc)
	name := s.CurrentOnCallName()
	if name != "carol" {
		t.Errorf("expected carol, got %q", name)
	}
}

// ── GetSchedule 深度副本测试 ───────────────────────────────────────────────────

func TestOncall_GetSchedule_DeepCopy(t *testing.T) {
	s := New()
	sc := &OnCallSchedule{
		Name:     "original",
		Rotation: []OnCallSlot{
			{UserName: "alice", StartHour: 9, EndHour: 17, Days: []string{"mon"}},
		},
	}
	s.SetSchedule(sc)

	// 修改返回的副本，不应影响存储中的数据
	got := s.GetSchedule()
	got.Name = "modified"
	got.Rotation[0].UserName = "hacker"

	original := s.GetSchedule()
	if original.Name == "modified" {
		t.Error("modifying returned schedule should not affect stored schedule")
	}
	if original.Rotation[0].UserName == "hacker" {
		t.Error("modifying returned rotation slot should not affect stored data")
	}
}
