package api

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type ipLimiter struct {
	mu      sync.Mutex
	entries map[string]*rlEntry
}

type rlEntry struct {
	count   int
	resetAt time.Time
}

func newIPLimiter() *ipLimiter {
	l := &ipLimiter{entries: make(map[string]*rlEntry)}
	go func() {
		for range time.Tick(5 * time.Minute) {
			l.mu.Lock()
			now := time.Now()
			for ip, e := range l.entries {
				if now.After(e.resetAt) {
					delete(l.entries, ip)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *ipLimiter) allow(ip string, maxReq int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.entries[ip]
	if !ok || now.After(e.resetAt) {
		l.entries[ip] = &rlEntry{count: 1, resetAt: now.Add(window)}
		return true
	}
	e.count++
	return e.count <= maxReq
}

func rateLimitMiddleware(limiter *ipLimiter, maxReq int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := realIP(r)
			if !limiter.allow(ip, maxReq, window) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func realIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.SplitN(forwarded, ",", 2)[0])
	}
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}
