// Package ratelimit provides in-memory token-bucket rate limiting.
//
// Two kinds of keys are tracked per HTTP request:
//
//   - per-key (e.g. username or IP) — strict, used to defend against
//     brute-force on a specific credential
//   - per-IP — looser, defends against credential stuffing across many
//     usernames from a single source
//
// Buckets auto-expire after `cleanup_after` so memory is bounded.
// All counters live in process memory; this is sufficient for a single-
// instance skygate deployment. If we ever go multi-instance, swap the
// store for Redis.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter holds the per-key/per-IP counters.
type Limiter struct {
	mu sync.Mutex

	// Per-second-style settings. We use "capacity" (max burst) and "refill
	// per second". Each Allow() consumes one token; tokens refill linearly.
	loginPerKeyCapacity   int
	loginPerKeyRefill     float64 // tokens per second
	loginPerIPCapacity    int
	loginPerIPRefill      float64
	apiPerIPCapacity      int
	apiPerIPRefill        float64

	loginCleanupAfter time.Duration
	cleanupAfter      time.Duration

	buckets map[string]*bucket
}

// bucket tracks a single key's token state and last access time.
type bucket struct {
	tokens    float64
	lastAllow time.Time
	lastSeen  time.Time
}

// New constructs a Limiter with conservative defaults for skygate.
//
//	login:    5 attempts per username per 15s, 20 per IP per 30s
//	api:      30 requests per IP per minute
//
// These are tuned for typical end-user behaviour: a human typing the
// wrong password a few times should not get blocked, but automated
// tooling at any rate above ~10 attempts/sec will hit limits.
func New() *Limiter {
	l := &Limiter{
		loginPerKeyCapacity: 5,
		loginPerKeyRefill:   5.0 / 15.0, // 5 per 15s
		loginPerIPCapacity:  20,
		loginPerIPRefill:    20.0 / 30.0,
		apiPerIPCapacity:    30,
		apiPerIPRefill:      30.0 / 60.0,
		loginCleanupAfter:   10 * time.Minute,
		cleanupAfter:        10 * time.Minute,
		buckets:             make(map[string]*bucket),
	}
	return l
}

// AllowLogin checks both the per-username and per-IP buckets. If either
// bucket is exhausted it returns false. refills before checking.
func (l *Limiter) AllowLogin(username, ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.consumeLocked(username, l.loginPerKeyCapacity, l.loginPerKeyRefill, now) {
		return false
	}
	if !l.consumeLocked("ip:"+ip, l.loginPerIPCapacity, l.loginPerIPRefill, now) {
		return false
	}
	return true
}

// AllowAPI enforces the per-IP bucket for API endpoints.
func (l *Limiter) AllowAPI(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.consumeLocked("api:"+ip, l.apiPerIPCapacity, l.apiPerIPRefill, now)
}

// consumeLocked is the lock-protected token operation. Callers must hold l.mu.
func (l *Limiter) consumeLocked(key string, cap int, refill float64, now time.Time) bool {
	cleanup := l.cleanupAfter
	if cleanup == 0 {
		cleanup = 10 * time.Minute
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: float64(cap)}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.lastAllow).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * refill
		if b.tokens > float64(cap) {
			b.tokens = float64(cap)
		}
	}
	b.lastAllow = now
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Sweep deletes stale bucket entries. Not strictly required for correctness
// but bounds memory. Call periodically from a background goroutine.
func (l *Limiter) Sweep() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.cleanupAfter {
			delete(l.buckets, k)
		}
	}
}

// ClientIP returns the IP that should be rate-limited. Prefers
// X-Forwarded-For (skygate is often behind a reverse proxy) but falls
// back to RemoteAddr. The result is a single canonical IP string or
// "unknown" if extraction fails.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = strings.TrimSpace(xff[:i])
		}
		if xff != "" {
			if ip := net.ParseIP(xff); ip != nil {
				return ip.String()
			}
		}
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		if ip := net.ParseIP(xrip); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
