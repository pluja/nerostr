package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRealIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("127.0.0.0/8")}
	cases := []struct {
		name, remote, xff, want string
	}{
		{"direct client ignores spoofed header", "198.51.100.1:5555", "1.2.3.4", "198.51.100.1"},
		{"behind proxy", "172.18.0.2:4444", "203.0.113.5", "203.0.113.5"},
		{"client-supplied prefix is ignored", "172.18.0.2:4444", "1.2.3.4, 203.0.113.5", "203.0.113.5"},
		{"chain of trusted proxies", "127.0.0.1:1", "203.0.113.5, 172.18.0.9", "203.0.113.5"},
		{"proxy without header", "172.18.0.2:4444", "", "172.18.0.2"},
		{"garbage header stops at last good hop", "172.18.0.2:4444", "nonsense", "172.18.0.2"},
		{"ipv4-mapped remote", "[::ffff:172.18.0.2]:4444", "203.0.113.7", "203.0.113.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotRemote, gotXFF string
			h := RealIP(trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRemote, gotXFF = clientIP(r), r.Header.Get("X-Forwarded-For")
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = c.remote
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			req.Header.Set("X-Real-Ip", "9.9.9.9")
			h.ServeHTTP(httptest.NewRecorder(), req)
			if gotRemote != c.want || gotXFF != c.want {
				t.Fatalf("got remote=%s xff=%s, want %s", gotRemote, gotXFF, c.want)
			}
		})
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(1<<62, 2)
	for i := range 2 {
		if !l.allow("a") {
			t.Fatalf("request %d of the burst should be allowed", i+1)
		}
	}
	if l.allow("a") {
		t.Fatal("third request should be limited")
	}
	if !l.allow("b") {
		t.Fatal("keys are independent")
	}
}
