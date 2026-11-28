package kosync

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter slows password guessing from one client address.
//
// The reader's paths can be public: Outpost's public_paths, put on the
// internet with Funnel, are the only way a reader that cannot join the tailnet
// reaches shelf. Anyone can then try passwords. Limiting by address rather
// than by account keeps a stranger who learns a username from locking the
// owner's own reader out; a generated password makes guessing hopeless
// anyway, and this keeps a weak hand-picked one from being guessed quickly.
type Limiter struct {
	// Max failures from one address within Window before it is refused.
	Max    int
	Window time.Duration

	mu    sync.Mutex
	fails map[string][]time.Time
	now   func() time.Time
}

// NewLimiter allows ten wrong passwords per address every fifteen minutes:
// plenty for someone mistyping on an e-ink keyboard, far too few to guess.
func NewLimiter() *Limiter {
	return &Limiter{Max: 10, Window: 15 * time.Minute, fails: map[string][]time.Time{}, now: time.Now}
}

// Allowed reports whether r's client may try a password now.
func (l *Limiter) Allowed(r *http.Request) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ClientIP(r))) < l.Max
}

// Failed records a wrong password from r's client.
func (l *Limiter) Failed(r *http.Request) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ip := ClientIP(r)
	l.fails[ip] = append(l.recent(ip), l.now())
	// Bound memory against a scan from many addresses: drop stale entries.
	if len(l.fails) > 4096 {
		for k := range l.fails {
			if len(l.recent(k)) == 0 {
				delete(l.fails, k)
			}
		}
	}
}

// Succeeded clears r's client after a right password.
func (l *Limiter) Succeeded(r *http.Request) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ClientIP(r))
}

// RetryAfter is how long to tell a refused client to wait, in seconds.
func (l *Limiter) RetryAfter() int { return int(l.Window / time.Second) }

func (l *Limiter) recent(ip string) []time.Time {
	cut := l.now().Add(-l.Window)
	kept := l.fails[ip][:0]
	for _, t := range l.fails[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, ip)
		return nil
	}
	l.fails[ip] = kept
	return kept
}

// ClientIP is the address a request came from. X-Forwarded-For is believed
// only from loopback: under Outpost's gateway and Tailscale Funnel every
// request arrives from the local proxy, which sets it to the real sender. A
// client connecting directly could write anything there, so for anyone else
// the connection's own address is used.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			if f := strings.TrimSpace(first); f != "" {
				return f
			}
		}
	}
	return host
}
