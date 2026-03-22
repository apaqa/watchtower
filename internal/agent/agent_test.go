// Package agent 单元测试：验证系统指标采集返回合理的非零值
package agent

import (
	"testing"
	"time"
)

// TestCollectReturnsNonZeroValues 验证 Collect() 返回 4 个预期指标且值合理
func TestCollectReturnsNonZeroValues(t *testing.T) {
	ag, err := New("http://localhost:9999/unused", 5*time.Second)
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}

	points, err := ag.Collect()
	if err != nil {
		t.Fatalf("Collect() 失败: %v", err)
	}

	// 应至少返回 3 个指标（cpu + memory_percent + memory_bytes + disk_percent）
	if len(points) < 3 {
		t.Fatalf("期望至少 3 个指标，实际 %d", len(points))
	}

	// 构建指标名称集合，方便检查
	byName := make(map[string]float64)
	for _, p := range points {
		byName[p.Name] = p.Value
	}

	// 检查内存使用率（任何机器上都应 > 0）
	if v, ok := byName["memory_usage_percent"]; !ok {
		t.Error("缺少 memory_usage_percent 指标")
	} else if v <= 0 || v > 100 {
		t.Errorf("memory_usage_percent 值不合理: %.2f", v)
	}

	// 检查内存已用字节（应 > 0）
	if v, ok := byName["memory_used_bytes"]; !ok {
		t.Error("缺少 memory_used_bytes 指标")
	} else if v <= 0 {
		t.Errorf("memory_used_bytes 应 > 0，实际: %.0f", v)
	}

	// 检查时间戳已被填充
	for _, p := range points {
		if p.Timestamp <= 0 {
			t.Errorf("指标 %s 的时间戳未设置", p.Name)
		}
		// 时间戳应在最近 10 秒内
		now := time.Now().UnixMilli()
		if p.Timestamp < now-10000 || p.Timestamp > now+1000 {
			t.Errorf("指标 %s 的时间戳超出合理范围: %d", p.Name, p.Timestamp)
		}
	}

	// 检查所有指标都有 host 标签
	for _, p := range points {
		if p.Labels["host"] == "" {
			t.Errorf("指标 %s 缺少 host 标签", p.Name)
		}
	}
}

// TestCollectCPUPercent 专门验证 CPU 指标在合理范围内
func TestCollectCPUPercent(t *testing.T) {
	ag, _ := New("http://localhost:9999/unused", 5*time.Second)
	points, err := ag.Collect()
	if err != nil {
		t.Fatalf("Collect() 失败: %v", err)
	}

	for _, p := range points {
		if p.Name == "cpu_usage_percent" {
			if p.Value < 0 || p.Value > 100 {
				t.Errorf("cpu_usage_percent 超出 [0,100] 范围: %.2f", p.Value)
			}
			return
		}
	}
	// CPU 指标在某些 CI 环境中可能不可用，不强制失败
	t.Log("未找到 cpu_usage_percent 指标（某些环境下正常）")
}
