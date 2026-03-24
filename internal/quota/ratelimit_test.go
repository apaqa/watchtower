package quota

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRateLimiter_AllowUnderCapacity 初始令牌充足时请求应通过
func TestRateLimiter_AllowUnderCapacity(t *testing.T) {
	rl := NewRateLimiter(BucketConfig{Capacity: 10, RefillRate: 1})
	rr := httptest.NewRecorder()
	if !rl.Allow(rr, "key1") {
		t.Error("期望首次请求通过")
	}
	if rr.Header().Get("X-RateLimit-Limit") == "" {
		t.Error("期望设置 X-RateLimit-Limit 响应头")
	}
	if rr.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("期望设置 X-RateLimit-Remaining 响应头")
	}
}

// TestRateLimiter_DenyWhenExhausted 令牌耗尽时请求应被拒绝
func TestRateLimiter_DenyWhenExhausted(t *testing.T) {
	rl := NewRateLimiter(BucketConfig{Capacity: 3, RefillRate: 0.001})
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl.SetNowFn(func() time.Time { return now })

	// 消耗所有令牌
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		rl.Allow(rr, "key1")
	}

	// 第 4 次应被拒绝（时间不变，不补充令牌）
	rr := httptest.NewRecorder()
	if rl.Allow(rr, "key1") {
		t.Error("期望令牌耗尽后被拒绝")
	}
	if rr.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("期望剩余令牌为 0，实际 %s", rr.Header().Get("X-RateLimit-Remaining"))
	}
}

// TestRateLimiter_RefillOverTime 时间流逝后令牌应自动补充
func TestRateLimiter_RefillOverTime(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(BucketConfig{Capacity: 5, RefillRate: 10}) // 每秒 10 个
	rl.SetNowFn(func() time.Time { return now })

	// 耗尽令牌
	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		rl.Allow(rr, "key2")
	}
	rr := httptest.NewRecorder()
	if rl.Allow(rr, "key2") {
		t.Error("期望令牌耗尽")
	}

	// 时间前进 1 秒，应补充 10 个令牌（上限 5）
	now = now.Add(time.Second)
	rl.SetNowFn(func() time.Time { return now })

	rr = httptest.NewRecorder()
	if !rl.Allow(rr, "key2") {
		t.Error("期望令牌补充后允许通过")
	}
}

// TestRateLimiter_IsolatedPerKey 不同 key 的令牌桶互相独立
func TestRateLimiter_IsolatedPerKey(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(BucketConfig{Capacity: 2, RefillRate: 0.001})
	rl.SetNowFn(func() time.Time { return now })

	// 耗尽 key_a
	for i := 0; i < 2; i++ {
		rl.Allow(httptest.NewRecorder(), "key_a")
	}
	rr := httptest.NewRecorder()
	if rl.Allow(rr, "key_a") {
		t.Error("key_a 应已耗尽")
	}

	// key_b 应不受影响
	rr2 := httptest.NewRecorder()
	if !rl.Allow(rr2, "key_b") {
		t.Error("key_b 应有独立配额，期望通过")
	}
}

// TestRateLimiter_Middleware_429 中间件在令牌耗尽时应返回 429
func TestRateLimiter_Middleware_429(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(BucketConfig{Capacity: 1, RefillRate: 0.001})
	rl.SetNowFn(func() time.Time { return now })

	handler := rl.Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		func(r *http.Request) string {
			return r.Header.Get("X-API-Key")
		},
	)

	// 第一次通过
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set("X-API-Key", "testkey")
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusOK {
		t.Errorf("期望第一次请求 200，实际 %d", rr1.Code)
	}

	// 第二次被限流
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("X-API-Key", "testkey")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusTooManyRequests {
		t.Errorf("期望第二次请求 429，实际 %d", rr2.Code)
	}
}
