// Package tsdb — 保留策略引擎：根据配置的正则匹配规则，定期清理过期数据，
// 并对序列长度施加上限约束。
//
// 内置策略（不可删除）：
//   - raw       匹配 ^[^:]+$ （无后缀的原始指标）  最大保留 1 小时
//   - 1m-ds     匹配 .*:1m$                        最大保留 24 小时
//   - 5m-ds     匹配 .*:5m$                        最大保留 7 天
package tsdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const retentionCheckInterval = 10 * time.Minute

// RetentionPolicy 描述保留策略规则
type RetentionPolicy struct {
	Name         string `json:"name"`
	MatchPattern string `json:"match_pattern"`         // 正则表达式，匹配指标名称
	MaxAgeSecs   int64  `json:"max_age_seconds"`       // 最大保留秒数；0 = 不限制
	MaxPoints    int    `json:"max_points_per_series"` // 最大数据点数；0 = 不限制
	Builtin      bool   `json:"builtin"`               // 内置策略（不可通过 API 删除）
	compiled     *regexp.Regexp
}

// RetentionEngine 管理保留策略并在后台定期执行清理
type RetentionEngine struct {
	mu       sync.RWMutex
	db       *TSDB
	policies []*RetentionPolicy
	stopCh   chan struct{}
}

// NewRetentionEngine 创建包含默认保留策略的保留引擎
func NewRetentionEngine(db *TSDB) *RetentionEngine {
	re := &RetentionEngine{
		db:     db,
		stopCh: make(chan struct{}),
	}
	// 内置策略：原始、1 分钟、5 分钟降采样
	re.policies = []*RetentionPolicy{
		compilePolicy(&RetentionPolicy{
			Name:         "raw",
			MatchPattern: `^[^:]+$`, // 不含冒号的原始指标
			MaxAgeSecs:   3600,      // 1 小时
			Builtin:      true,
		}),
		compilePolicy(&RetentionPolicy{
			Name:         "1m-ds",
			MatchPattern: `.*:1m$`,
			MaxAgeSecs:   86400, // 24 小时
			Builtin:      true,
		}),
		compilePolicy(&RetentionPolicy{
			Name:         "5m-ds",
			MatchPattern: `.*:5m$`,
			MaxAgeSecs:   7 * 86400, // 7 天
			Builtin:      true,
		}),
	}
	return re
}

// compilePolicy 编译策略中的正则表达式并返回指针（panic on invalid regex）
func compilePolicy(p *RetentionPolicy) *RetentionPolicy {
	p.compiled = regexp.MustCompile(p.MatchPattern)
	return p
}

// Start 启动后台策略执行循环
func (re *RetentionEngine) Start() {
	go re.loop()
}

// Stop 停止后台循环
func (re *RetentionEngine) Stop() {
	close(re.stopCh)
}

func (re *RetentionEngine) loop() {
	ticker := time.NewTicker(retentionCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			re.EnforceAll()
		case <-re.stopCh:
			return
		}
	}
}

// EnforceAll 对所有序列应用匹配的保留策略（可手动触发，也用于测试）
func (re *RetentionEngine) EnforceAll() {
	re.db.mu.RLock()
	series := make([]*Series, 0, len(re.db.series))
	for _, s := range re.db.series {
		series = append(series, s)
	}
	re.db.mu.RUnlock()

	re.mu.RLock()
	policies := make([]*RetentionPolicy, len(re.policies))
	copy(policies, re.policies)
	re.mu.RUnlock()

	for _, s := range series {
		pol := matchPolicy(policies, s.Name)
		if pol == nil {
			continue
		}
		if pol.MaxAgeSecs > 0 {
			cutoff := time.Now().Add(-time.Duration(pol.MaxAgeSecs) * time.Second).UnixMilli()
			s.Cleanup(cutoff)
		}
		if pol.MaxPoints > 0 {
			s.TrimToLength(pol.MaxPoints)
		}
	}
}

// matchPolicy 返回第一个 MatchPattern 匹配 name 的策略（优先使用非内置自定义策略）
func matchPolicy(policies []*RetentionPolicy, name string) *RetentionPolicy {
	// 先检查自定义（非内置）策略
	for _, p := range policies {
		if !p.Builtin && p.compiled != nil && p.compiled.MatchString(name) {
			return p
		}
	}
	// 再回退到内置策略
	for _, p := range policies {
		if p.Builtin && p.compiled != nil && p.compiled.MatchString(name) {
			return p
		}
	}
	return nil
}

// AddPolicy 添加一条自定义保留策略；若名称重复或正则无效则返回错误
func (re *RetentionEngine) AddPolicy(p RetentionPolicy) error {
	if p.Name == "" {
		return errors.New("policy name is required")
	}
	if p.MatchPattern == "" {
		return errors.New("match_pattern is required")
	}
	compiled, err := regexp.Compile(p.MatchPattern)
	if err != nil {
		return fmt.Errorf("invalid match_pattern: %w", err)
	}
	p.compiled = compiled
	p.Builtin = false // 通过 API 创建的策略均为自定义策略

	re.mu.Lock()
	defer re.mu.Unlock()
	for _, existing := range re.policies {
		if existing.Name == p.Name {
			return errors.New("policy already exists: " + p.Name)
		}
	}
	re.policies = append(re.policies, &p)
	return nil
}

// DeletePolicy 删除指定名称的自定义策略；内置策略不可删除
func (re *RetentionEngine) DeletePolicy(name string) error {
	re.mu.Lock()
	defer re.mu.Unlock()
	for i, p := range re.policies {
		if p.Name == name {
			if p.Builtin {
				return errors.New("cannot delete built-in policy: " + name)
			}
			re.policies = append(re.policies[:i], re.policies[i+1:]...)
			return nil
		}
	}
	return errors.New("policy not found: " + name)
}

// ListPolicies 返回所有当前策略的副本
func (re *RetentionEngine) ListPolicies() []RetentionPolicy {
	re.mu.RLock()
	defer re.mu.RUnlock()
	result := make([]RetentionPolicy, len(re.policies))
	for i, p := range re.policies {
		result[i] = *p
		result[i].compiled = nil // 不序列化编译后的正则
	}
	return result
}

// RegisterRetentionRoutes 在给定 ServeMux 上注册保留策略 CRUD API
func RegisterRetentionRoutes(mux *http.ServeMux, re *RetentionEngine) {
	// GET /api/v1/retention  — 列出所有策略
	// POST /api/v1/retention — 创建自定义策略
	mux.HandleFunc("/api/v1/retention", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(re.ListPolicies())
		case http.MethodPost:
			var p RetentionPolicy
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
				return
			}
			if err := re.AddPolicy(p); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// DELETE /api/v1/retention/{name} — 删除指定自定义策略
	mux.HandleFunc("/api/v1/retention/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/retention/")
		if name == "" {
			http.Error(w, `{"error":"policy name required"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := re.DeletePolicy(name); err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
