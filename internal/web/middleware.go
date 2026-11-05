package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RealIP resolves the client address from X-Forwarded-For, trusting only hops
// that come from trusted proxies, and rewrites the request so that downstream
// code (including khatru's own rate limits) sees just that address.
func RealIP(trusted []netip.Prefix, next http.Handler) http.Handler {
	isTrusted := func(a netip.Addr) bool {
		a = a.Unmap()
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := remoteAddr(r)
		if client.IsValid() && isTrusted(client) {
			hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
			for i := len(hops) - 1; i >= 0; i-- {
				hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
				if err != nil {
					break
				}
				client = hop.Unmap()
				if !isTrusted(client) {
					break
				}
			}
		}
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("X-Real-Ip")
		if client.IsValid() {
			r.Header.Set("X-Forwarded-For", client.String())
			r.RemoteAddr = net.JoinHostPort(client.String(), "0")
		}
		next.ServeHTTP(w, r)
	})
}

func remoteAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err == nil {
		return ap.Addr().Unmap()
	}
	a, _ := netip.ParseAddr(r.RemoteAddr)
	return a.Unmap()
}

func clientIP(r *http.Request) string {
	if a := remoteAddr(r); a.IsValid() {
		return a.String()
	}
	return r.RemoteAddr
}

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// LimitBody caps request bodies, including NIP-86 calls which khatru reads unbounded.
func LimitBody(max int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// limiter is a per-key token bucket with idle entries evicted periodically.
type limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	buckets map[string]*bucket
}

type bucket struct {
	l    *rate.Limiter
	seen time.Time
}

func newLimiter(interval time.Duration, burst int) *limiter {
	l := &limiter{every: rate.Every(interval), burst: burst, buckets: map[string]*bucket{}}
	go func() {
		for range time.Tick(10 * time.Minute) {
			l.mu.Lock()
			for k, b := range l.buckets {
				if time.Since(b.seen) > time.Hour {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{l: rate.NewLimiter(l.every, l.burst)}
		l.buckets[key] = b
	}
	b.seen = time.Now()
	return b.l.Allow()
}
