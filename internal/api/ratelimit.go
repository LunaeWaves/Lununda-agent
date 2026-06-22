package api

import (
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a simple per-user sliding-window rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time // userID → request timestamps
	rpm     int                    // requests per minute (0 = unlimited)
	window  time.Duration
}

func newRateLimiter(rpm int) *rateLimiter {
	if rpm <= 0 {
		return &rateLimiter{rpm: 0}
	}
	return &rateLimiter{
		windows: make(map[string][]time.Time),
		rpm:     rpm,
		window:  time.Minute,
	}
}

// allow returns true if the request for userID should be permitted.
func (rl *rateLimiter) allow(userID string) bool {
	if rl.rpm <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Lazy sweep: once the user map grows past maxMapSize, purge
	// fully-expired keys inline so inactive users don't accumulate
	// forever. No dedicated cleanup goroutine — the sweep runs on the
	// request that crosses the threshold. ponytail: inline O(n) sweep
	// under the lock; replace with a periodic cleanup goroutine if this
	// shows up in profiles or active users routinely exceed the cap.
	const maxMapSize = 4096
	if len(rl.windows) > maxMapSize {
		for uid, st := range rl.windows {
			i := 0
			for i < len(st) && st[i].Before(cutoff) {
				i++
			}
			if i == len(st) {
				delete(rl.windows, uid)
			} else {
				rl.windows[uid] = st[i:]
			}
		}
	}

	// Prune expired entries.
	ts := rl.windows[userID]
	start := 0
	for start < len(ts) && ts[start].Before(cutoff) {
		start++
	}
	ts = ts[start:]

	if len(ts) >= rl.rpm {
		rl.windows[userID] = ts
		return false
	}
	rl.windows[userID] = append(ts, now)
	return true
}

// rateLimitMiddleware wraps a handler and returns 429 when a user exceeds
// the configured RPM.
func rateLimitMiddleware(rl *rateLimiter, getUserID func(r *http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	if rl == nil || rl.rpm <= 0 {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUserID(r)
		if !rl.allow(uid) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": map[string]string{
					"message": "rate limit exceeded — try again shortly",
					"type":    "rate_limit_error",
				},
			})
			return
		}
		next(w, r)
	}
}
