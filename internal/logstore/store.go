// Package logstore 提供基于内存环形缓冲区的日志存储与搜索功能
package logstore

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

const (
	// maxPerSource 是每个来源保留的最大日志条数（超出时丢弃最旧的）
	maxPerSource = 10000
	// retentionDuration 是日志最长保留时长
	retentionDuration = 24 * time.Hour
)

// Store 是线程安全的内存日志存储，按来源维护独立的环形缓冲区
type Store struct {
	mu      sync.RWMutex
	sources map[string][]*model.LogEntry // 来源名称 → 日志条目切片（按时间升序，最旧在前）
	stopCh  chan struct{}
}

// New 创建并启动日志存储实例；后台 goroutine 负责定期清理过期日志
func New() *Store {
	s := &Store{
		sources: make(map[string][]*model.LogEntry),
		stopCh:  make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// Stop 停止后台清理 goroutine
func (s *Store) Stop() {
	close(s.stopCh)
}

// Write 写入一条日志记录（自动填充缺失的时间戳和来源）
func (s *Store) Write(entry model.LogEntry) {
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}
	if entry.Source == "" {
		entry.Source = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLocked(entry)
}

// WriteMany 批量写入日志记录
func (s *Store) WriteMany(entries []model.LogEntry) {
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		if e.Timestamp == 0 {
			e.Timestamp = now
		}
		if e.Source == "" {
			e.Source = "unknown"
		}
		s.appendLocked(e)
	}
}

// appendLocked 在持有写锁的情况下追加一条日志（调用方必须持有写锁）
func (s *Store) appendLocked(entry model.LogEntry) {
	buf := s.sources[entry.Source]
	buf = append(buf, &entry)
	// 环形缓冲区：超过上限时丢弃最旧的条目
	if len(buf) > maxPerSource {
		buf = buf[len(buf)-maxPerSource:]
	}
	s.sources[entry.Source] = buf
}

// SearchOptions 是日志搜索的过滤条件
type SearchOptions struct {
	Query  string         // 搜索文本；以 / 开头和结尾表示正则表达式
	Level  model.LogLevel // 空值表示不过滤级别
	Source string         // 空值表示不过滤来源
	Start  int64          // Unix 毫秒，0 表示不限起始时间
	End    int64          // Unix 毫秒，0 表示不限结束时间
	Limit  int            // 返回条数上限，0 使用默认值 100
}

// Search 按条件搜索日志，返回最新的匹配结果（按时间降序排列）
func (s *Store) Search(opts SearchOptions) []model.LogEntry {
	if opts.Limit <= 0 {
		opts.Limit = 100
	}

	// 判断是否为正则搜索（以 / 开头和结尾，且长度至少为 2）
	var re *regexp.Regexp
	isRegex := len(opts.Query) >= 2 && opts.Query[0] == '/' && opts.Query[len(opts.Query)-1] == '/'
	if isRegex {
		pattern := opts.Query[1 : len(opts.Query)-1]
		re, _ = regexp.Compile(pattern) // 编译失败时 re 为 nil，后续逻辑视为无匹配
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.LogEntry
	for src, buf := range s.sources {
		// 来源过滤
		if opts.Source != "" && src != opts.Source {
			continue
		}
		for _, entry := range buf {
			// 级别过滤
			if opts.Level != "" && entry.Level != opts.Level {
				continue
			}
			// 时间范围过滤
			if opts.Start != 0 && entry.Timestamp < opts.Start {
				continue
			}
			if opts.End != 0 && entry.Timestamp > opts.End {
				continue
			}
			// 消息内容过滤（全文或正则）
			if opts.Query != "" {
				if isRegex {
					if re == nil || !re.MatchString(entry.Message) {
						continue
					}
				} else if !strings.Contains(strings.ToLower(entry.Message), strings.ToLower(opts.Query)) {
					continue
				}
			}
			result = append(result, *entry)
		}
	}

	// 按时间戳降序排列（最新的在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp > result[j].Timestamp
	})

	// 截断到上限
	if len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result
}

// Recent 返回最近 n 条日志（不过滤来源，按时间降序）
func (s *Store) Recent(n int) []model.LogEntry {
	return s.Search(SearchOptions{Limit: n})
}

// TotalCount 返回所有来源的日志总条数（供测试和监控使用）
func (s *Store) TotalCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, buf := range s.sources {
		total += len(buf)
	}
	return total
}

// cleanupLoop 每 5 分钟触发一次过期日志清理
func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

// cleanup 删除所有来源中超过 retentionDuration 的旧条目
func (s *Store) cleanup() {
	cutoff := time.Now().Add(-retentionDuration).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	for src, buf := range s.sources {
		// 找到第一条在保留窗口内的条目（切片按时间升序排列）
		i := 0
		for i < len(buf) && buf[i].Timestamp < cutoff {
			i++
		}
		if i > 0 {
			s.sources[src] = buf[i:]
		}
		if len(s.sources[src]) == 0 {
			delete(s.sources, src)
		}
	}
}
