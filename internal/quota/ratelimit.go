// 令牌桶速率限制器，支持按 API 密钥独立限速
package quota

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// BucketConfig 令牌桶参数配置
type BucketConfig struct {
	Capacity   float64 // 桶容量（突发上限）
	RefillRate float64 // 每秒补充令牌数
}

// DefaultBucketConfig 默认速率限制：每秒 100 请求，突发 200
var DefaultBucketConfig = BucketConfig{
	Capacity:   200,
	RefillRate: 100,
}

// bucket 单个 API 密钥的令牌桶状态
type bucket struct {
	mu         sync.Mutex
	tokens     float64
	capacity   float64
	refillRate float64 // tokens/sec
	lastRefill time.Time
}

// take 尝试消耗 1 个令牌，返回 (是否成功, 剩余令牌数)
func (b *bucket) take(now time.Time) (bool, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 先补充令牌
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastRefill = now

	if b.tokens < 1 {
		remaining := int64(b.tokens)
		if remaining < 0 {
			remaining = 0
		}
		return false, remaining
	}
	b.tokens--
	return true, int64(b.tokens)
}

// RateLimiter 管理所有 API 密钥的令牌桶
type RateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*bucket // key → bucket
	cfg     BucketConfig
	nowFn   func() time.Time
}

// NewRateLimiter 创建一个速率限制器
func NewRateLimiter(cfg BucketConfig) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		cfg:     cfg,
		nowFn:   time.Now,
	}
}

// SetNowFn 注入自定义时间函数（测试专用）
func (rl *RateLimiter) SetNowFn(fn func() time.Time) {
	rl.nowFn = fn
}

// getBucket 获取或创建指定 key 的令牌桶（懒初始化）
func (rl *RateLimiter) getBucket(key string) *bucket {
	rl.mu.RLock()
	b, ok := rl.buckets[key]
	rl.mu.RUnlock()
	if ok {
		return b
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	// 双重检查
	if b, ok = rl.buckets[key]; ok {
		return b
	}
	b = &bucket{
		tokens:     rl.cfg.Capacity,
		capacity:   rl.cfg.Capacity,
		refillRate: rl.cfg.RefillRate,
		lastRefill: rl.nowFn(),
	}
	rl.buckets[key] = b
	return b
}

// Allow 检查指定 key 是否允许通过，同时写入速率限制响应头
// key 为空时使用 "anonymous" 桶
func (rl *RateLimiter) Allow(w http.ResponseWriter, key string) bool {
	if key == "" {
		key = "anonymous"
	}

	b := rl.getBucket(key)
	now := rl.nowFn()
	ok, remaining := b.take(now)

	// 计算下次令牌补充时间（重置时间戳，秒精度）
	resetSec := now.Add(time.Second).Unix()

	w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", rl.cfg.Capacity))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetSec))

	return ok
}

// Middleware 返回一个 HTTP 中间件，对每个请求执行速率检查
// keyFn 从请求中提取 API key（可返回空字符串表示匿名）
func (rl *RateLimiter) Middleware(next http.Handler, keyFn func(r *http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyFn(r)
		if !rl.Allow(w, key) {
			http.Error(w, `{"error":"请求过于频繁，请稍后重试"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
