// Package oncall 提供值班排班管理功能：设置和查询当前值班人员。
package oncall

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// OnCallSlot 定义一个值班时间段
type OnCallSlot struct {
	UserName  string   `json:"user_name"`  // 值班人员姓名
	StartHour int      `json:"start_hour"` // 班次起始小时（0-23，含）
	EndHour   int      `json:"end_hour"`   // 班次结束小时（0-24，不含；24 表示午夜）
	Days      []string `json:"days"`       // 适用的星期：mon/tue/wed/thu/fri/sat/sun
}

// OnCallSchedule 定义值班排班表
type OnCallSchedule struct {
	Name     string       `json:"name"`     // 排班表名称
	Rotation []OnCallSlot `json:"rotation"` // 所有时间段列表
	Timezone string       `json:"timezone"` // IANA 时区（如 "UTC"、"Asia/Shanghai"），空则默认 UTC
}

// OnCallInfo 当前值班信息（API 响应用）
type OnCallInfo struct {
	User    string `json:"user"`     // 当前值班人；空字符串表示无人值班
	Until   int64  `json:"until_ms"` // 班次结束时间（Unix 毫秒）；0 表示无数据
	HasOnCall bool `json:"has_oncall"`
}

// Scheduler 管理值班排班（线程安全）
type Scheduler struct {
	mu       sync.RWMutex
	schedule *OnCallSchedule
}

// New 创建排班管理器实例
func New() *Scheduler {
	return &Scheduler{}
}

// SetSchedule 设置或替换当前排班表（含基本校验）
func (s *Scheduler) SetSchedule(schedule *OnCallSchedule) error {
	if schedule == nil {
		return fmt.Errorf("排班表不能为空")
	}
	for _, slot := range schedule.Rotation {
		if slot.UserName == "" {
			return fmt.Errorf("值班人员名称不能为空")
		}
		if slot.StartHour < 0 || slot.StartHour > 23 {
			return fmt.Errorf("起始小时 %d 超出范围 [0, 23]", slot.StartHour)
		}
		if slot.EndHour <= 0 || slot.EndHour > 24 {
			return fmt.Errorf("结束小时 %d 超出范围 [1, 24]", slot.EndHour)
		}
		if slot.StartHour >= slot.EndHour {
			return fmt.Errorf("起始小时（%d）必须小于结束小时（%d）", slot.StartHour, slot.EndHour)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := copySchedule(schedule)
	s.schedule = cp
	return nil
}

// GetSchedule 返回当前排班表副本；若未设置则返回 nil
func (s *Scheduler) GetSchedule() *OnCallSchedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.schedule == nil {
		return nil
	}
	return copySchedule(s.schedule)
}

// CurrentOnCall 返回当前值班信息：值班人姓名与班次结束时间（Unix 毫秒）。
// 若未设置排班表或当前无匹配时间段，返回空用户名和 0。
func (s *Scheduler) CurrentOnCall() OnCallInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.schedule == nil || len(s.schedule.Rotation) == 0 {
		return OnCallInfo{}
	}

	loc := time.UTC
	if s.schedule.Timezone != "" {
		if l, err := time.LoadLocation(s.schedule.Timezone); err == nil {
			loc = l
		}
	}

	now := time.Now().In(loc)
	hour := now.Hour()
	dayStr := strings.ToLower(now.Weekday().String()[:3]) // "mon", "tue", ...

	for _, slot := range s.schedule.Rotation {
		if hour < slot.StartHour || hour >= slot.EndHour {
			continue
		}
		for _, d := range slot.Days {
			if strings.ToLower(d) == dayStr {
				endH := slot.EndHour
				if endH == 24 {
					endH = 0
				}
				var endTime time.Time
				if slot.EndHour == 24 {
					// 次日 00:00
					endTime = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
				} else {
					endTime = time.Date(now.Year(), now.Month(), now.Day(), endH, 0, 0, 0, loc)
				}
				return OnCallInfo{
					User:    slot.UserName,
					Until:   endTime.UnixMilli(),
					HasOnCall: true,
				}
			}
		}
	}
	return OnCallInfo{}
}

// CurrentOnCallName 仅返回当前值班人员姓名（便于注入 incident.Store.SetOnCallFn）
func (s *Scheduler) CurrentOnCallName() string {
	return s.CurrentOnCall().User
}

// copySchedule 创建排班表深度副本
func copySchedule(sc *OnCallSchedule) *OnCallSchedule {
	cp := OnCallSchedule{
		Name:     sc.Name,
		Timezone: sc.Timezone,
		Rotation: make([]OnCallSlot, len(sc.Rotation)),
	}
	for i, slot := range sc.Rotation {
		days := make([]string, len(slot.Days))
		copy(days, slot.Days)
		cp.Rotation[i] = OnCallSlot{
			UserName:  slot.UserName,
			StartHour: slot.StartHour,
			EndHour:   slot.EndHour,
			Days:      days,
		}
	}
	return &cp
}
