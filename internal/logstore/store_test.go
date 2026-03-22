// Package logstore 单元测试：验证日志存储与搜索行为
package logstore

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

func makeEntry(source string, level model.LogLevel, msg string, tsOffsetMs int64) model.LogEntry {
	return model.LogEntry{
		Timestamp: time.Now().Add(time.Duration(tsOffsetMs) * time.Millisecond).UnixMilli(),
		Level:     level,
		Source:    source,
		Message:   msg,
	}
}

// TestWriteAndSearch 验证写入后可以通过无过滤条件的搜索检索到日志
func TestWriteAndSearch(t *testing.T) {
	s := New()
	defer s.Stop()

	s.Write(makeEntry("app1", model.LogLevelInfo, "server started", 0))
	s.Write(makeEntry("app1", model.LogLevelError, "connection refused", 0))
	s.Write(makeEntry("app2", model.LogLevelWarn, "high memory usage", 0))

	results := s.Search(SearchOptions{Limit: 100})
	if len(results) != 3 {
		t.Errorf("期望 3 条日志，实际 %d 条", len(results))
	}
}

// TestLevelFilter 验证级别过滤只返回指定级别的日志
func TestLevelFilter(t *testing.T) {
	s := New()
	defer s.Stop()

	s.Write(makeEntry("svc", model.LogLevelInfo, "info message", 0))
	s.Write(makeEntry("svc", model.LogLevelError, "error message", 0))
	s.Write(makeEntry("svc", model.LogLevelWarn, "warn message", 0))
	s.Write(makeEntry("svc", model.LogLevelDebug, "debug message", 0))

	errors := s.Search(SearchOptions{Level: model.LogLevelError, Limit: 100})
	if len(errors) != 1 {
		t.Errorf("过滤 error 级别期望 1 条，实际 %d 条", len(errors))
	}
	if errors[0].Message != "error message" {
		t.Errorf("期望 error message，实际 %q", errors[0].Message)
	}

	infos := s.Search(SearchOptions{Level: model.LogLevelInfo, Limit: 100})
	if len(infos) != 1 {
		t.Errorf("过滤 info 级别期望 1 条，实际 %d 条", len(infos))
	}
}

// TestSourceFilter 验证来源过滤只返回指定来源的日志
func TestSourceFilter(t *testing.T) {
	s := New()
	defer s.Stop()

	s.Write(makeEntry("frontend", model.LogLevelInfo, "page loaded", 0))
	s.Write(makeEntry("backend", model.LogLevelInfo, "request received", 0))
	s.Write(makeEntry("backend", model.LogLevelError, "database error", 0))

	results := s.Search(SearchOptions{Source: "backend", Limit: 100})
	if len(results) != 2 {
		t.Errorf("过滤 backend 来源期望 2 条，实际 %d 条", len(results))
	}
	for _, r := range results {
		if r.Source != "backend" {
			t.Errorf("期望来源 backend，实际 %q", r.Source)
		}
	}
}

// TestFullTextSearch 验证全文搜索（大小写不敏感）
func TestFullTextSearch(t *testing.T) {
	s := New()
	defer s.Stop()

	s.Write(makeEntry("svc", model.LogLevelInfo, "CPU spike detected: 85%", 0))
	s.Write(makeEntry("svc", model.LogLevelInfo, "Memory pressure: swap activated", 0))
	s.Write(makeEntry("svc", model.LogLevelInfo, "Disk cleanup completed", 0))

	// 大小写不敏感匹配
	results := s.Search(SearchOptions{Query: "cpu", Limit: 100})
	if len(results) != 1 {
		t.Errorf("搜索 'cpu' 期望 1 条，实际 %d 条", len(results))
	}

	// 部分匹配
	results = s.Search(SearchOptions{Query: "pressure", Limit: 100})
	if len(results) != 1 {
		t.Errorf("搜索 'pressure' 期望 1 条，实际 %d 条", len(results))
	}
}

// TestRegexSearch 验证正则表达式搜索（以 / 包围的模式）
func TestRegexSearch(t *testing.T) {
	s := New()
	defer s.Stop()

	s.Write(makeEntry("svc", model.LogLevelError, "error code 500", 0))
	s.Write(makeEntry("svc", model.LogLevelError, "error code 404", 0))
	s.Write(makeEntry("svc", model.LogLevelInfo, "request completed in 200ms", 0))

	// 正则匹配：error code 后接数字
	results := s.Search(SearchOptions{Query: "/error code \\d+/", Limit: 100})
	if len(results) != 2 {
		t.Errorf("正则搜索期望 2 条，实际 %d 条", len(results))
	}

	// 以数字结尾的正则
	results = s.Search(SearchOptions{Query: "/\\d+ms/", Limit: 100})
	if len(results) != 1 {
		t.Errorf("正则搜索 \\d+ms 期望 1 条，实际 %d 条", len(results))
	}
}

// TestRingBufferOverflow 验证每个来源超过 maxPerSource 后旧条目被丢弃
func TestRingBufferOverflow(t *testing.T) {
	s := New()
	defer s.Stop()

	// 写入 maxPerSource + 100 条，确保旧条目被淘汰
	for i := 0; i < maxPerSource+100; i++ {
		s.Write(model.LogEntry{
			Timestamp: int64(i + 1), // 时间戳单调递增
			Level:     model.LogLevelInfo,
			Source:    "overflow-test",
			Message:   fmt.Sprintf("log line %d", i),
		})
	}

	// 总条数应被截断到 maxPerSource
	count := s.TotalCount()
	if count != maxPerSource {
		t.Errorf("溢出后期望 %d 条，实际 %d 条", maxPerSource, count)
	}

	// 最新的 100 条应该能被检索到（Search 返回最新的）
	results := s.Search(SearchOptions{Source: "overflow-test", Limit: 100})
	if len(results) != 100 {
		t.Errorf("期望检索到 100 条，实际 %d 条", len(results))
	}
	// 最新的日志消息编号应接近上限
	if results[0].Message != fmt.Sprintf("log line %d", maxPerSource+99) {
		t.Errorf("期望最新消息为 log line %d，实际 %q", maxPerSource+99, results[0].Message)
	}
}

// TestTimeRangeFilter 验证时间范围过滤
func TestTimeRangeFilter(t *testing.T) {
	s := New()
	defer s.Stop()

	base := time.Now().UnixMilli()
	s.Write(model.LogEntry{Timestamp: base - 3000, Level: model.LogLevelInfo, Source: "svc", Message: "old entry"})
	s.Write(model.LogEntry{Timestamp: base - 1000, Level: model.LogLevelInfo, Source: "svc", Message: "recent entry"})
	s.Write(model.LogEntry{Timestamp: base + 1000, Level: model.LogLevelInfo, Source: "svc", Message: "future entry"})

	// 只取 base-2000 到 base 之间的条目
	results := s.Search(SearchOptions{Start: base - 2000, End: base, Limit: 100})
	if len(results) != 1 {
		t.Errorf("时间范围过滤期望 1 条，实际 %d 条", len(results))
	}
	if results[0].Message != "recent entry" {
		t.Errorf("期望 recent entry，实际 %q", results[0].Message)
	}
}

// TestSortDescending 验证搜索结果按时间戳降序（最新在前）排列
func TestSortDescending(t *testing.T) {
	s := New()
	defer s.Stop()

	base := time.Now().UnixMilli()
	s.Write(model.LogEntry{Timestamp: base + 100, Level: model.LogLevelInfo, Source: "svc", Message: "newest"})
	s.Write(model.LogEntry{Timestamp: base - 100, Level: model.LogLevelInfo, Source: "svc", Message: "oldest"})
	s.Write(model.LogEntry{Timestamp: base, Level: model.LogLevelInfo, Source: "svc", Message: "middle"})

	results := s.Search(SearchOptions{Limit: 100})
	if len(results) != 3 {
		t.Fatalf("期望 3 条，实际 %d 条", len(results))
	}
	if results[0].Message != "newest" {
		t.Errorf("第 1 条期望 newest，实际 %q", results[0].Message)
	}
	if results[2].Message != "oldest" {
		t.Errorf("第 3 条期望 oldest，实际 %q", results[2].Message)
	}
}

// TestWriteMany 验证批量写入
func TestWriteMany(t *testing.T) {
	s := New()
	defer s.Stop()

	entries := []model.LogEntry{
		{Level: model.LogLevelInfo, Source: "batch", Message: "batch msg 1"},
		{Level: model.LogLevelWarn, Source: "batch", Message: "batch msg 2"},
		{Level: model.LogLevelError, Source: "batch", Message: "batch msg 3"},
	}
	s.WriteMany(entries)

	results := s.Search(SearchOptions{Source: "batch", Limit: 100})
	if len(results) != 3 {
		t.Errorf("批量写入期望 3 条，实际 %d 条", len(results))
	}
}

// TestConcurrentWrite 验证并发写入不会导致数据竞争（使用 -race 标志检测）
func TestConcurrentWrite(t *testing.T) {
	s := New()
	defer s.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Write(model.LogEntry{
				Level:   model.LogLevelInfo,
				Source:  fmt.Sprintf("worker-%d", n%5),
				Message: fmt.Sprintf("concurrent write %d", n),
			})
		}(i)
	}
	wg.Wait()

	results := s.Search(SearchOptions{Limit: 200})
	if len(results) != 50 {
		t.Errorf("并发写入期望 50 条，实际 %d 条", len(results))
	}
}

// TestCleanup 验证 cleanup 删除时间戳早于保留时限的条目
func TestCleanup(t *testing.T) {
	s := New()
	defer s.Stop()

	// 写入一条"很旧"的日志（超过 24 小时前）
	old := model.LogEntry{
		Timestamp: time.Now().Add(-25 * time.Hour).UnixMilli(),
		Level:     model.LogLevelInfo,
		Source:    "cleanup-test",
		Message:   "ancient log",
	}
	// 写入一条新日志
	fresh := model.LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     model.LogLevelInfo,
		Source:    "cleanup-test",
		Message:   "fresh log",
	}
	s.Write(old)
	s.Write(fresh)

	if s.TotalCount() != 2 {
		t.Fatalf("清理前期望 2 条，实际 %d 条", s.TotalCount())
	}

	// 手动触发清理
	s.cleanup()

	results := s.Search(SearchOptions{Source: "cleanup-test", Limit: 100})
	if len(results) != 1 {
		t.Errorf("清理后期望 1 条，实际 %d 条", len(results))
	}
	if results[0].Message != "fresh log" {
		t.Errorf("清理后期望保留 fresh log，实际 %q", results[0].Message)
	}
}

// TestRecent 验证 Recent 返回最近 n 条日志
func TestRecent(t *testing.T) {
	s := New()
	defer s.Stop()

	for i := 0; i < 20; i++ {
		s.Write(model.LogEntry{
			Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond).UnixMilli(),
			Level:     model.LogLevelInfo,
			Source:    "svc",
			Message:   fmt.Sprintf("msg %d", i),
		})
	}

	results := s.Recent(5)
	if len(results) != 5 {
		t.Errorf("Recent(5) 期望 5 条，实际 %d 条", len(results))
	}
}
