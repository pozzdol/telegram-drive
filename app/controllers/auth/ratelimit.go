package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Failed-login limiter: after maxFails failures within window, the IP or the username is
// refused until the window passes. Counting both stops one IP guessing many accounts and
// many IPs guessing one account.
// ponytail: in memory, so a restart clears it; move to the database if several instances run.
const (
	maxFails = 10
	window   = 15 * time.Minute
)

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var logins = &limiter{hits: map[string][]time.Time{}}

var dummyHash, _ = hashPassword("not a real password")

func (l *limiter) recent(key string, now time.Time) []time.Time {
	ts := l.hits[key]
	for len(ts) > 0 && now.Sub(ts[0]) > window {
		ts = ts[1:]
	}
	if len(ts) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = ts
	}
	return ts
}

// blocked returns how long to wait, or 0 if the attempt may go ahead.
func (l *limiter) blocked(ip, user string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	var wait time.Duration
	for _, k := range []string{"ip:" + ip, "user:" + strings.ToLower(user)} {
		if ts := l.recent(k, now); len(ts) >= maxFails {
			wait = max(wait, window-now.Sub(ts[0]))
		}
	}
	return wait
}

func (l *limiter) fail(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range []string{"ip:" + ip, "user:" + strings.ToLower(user)} {
		l.hits[k] = append(l.recent(k, now), now)
	}
}

func (l *limiter) reset(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, "ip:"+ip)
	delete(l.hits, "user:"+strings.ToLower(user))
}

// clientIP trusts CF-Connecting-IP, which Cloudflare Tunnel sets; the API listens on
// 127.0.0.1, so nobody can reach it without going through the tunnel.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
