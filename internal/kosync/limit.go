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

// Succeeded is a right password from r's client. It deliberately forgives
// nothing: if it cleared the address, anyone holding one working account —
// their own reader's — could sign in with it after every nine guesses and
// guess at everyone else's forever. Failures age out of the window instead,
// which is soon enough for someone who mistyped.
func (l *Limiter) Succeeded(*http.Request) {}

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

// ClientIP is the address a request came from, as the limiter counts it.
//
// X-Forwarded-For is believed only from loopback: under Outpost's gateway and
// Tailscale Funnel every request arrives from a local proxy, which adds the
// real sender. A client connecting directly could write anything there, so
// for anyone else the connection's own address is used.
//
// The last entry that is not loopback is taken, not the first. A proxy
// appends what it saw to whatever the client sent, so the first entry is the
// client's own invention: trusting it let anyone on the internet claim a new
// address with every guess. Entries to the right of the real sender are only
// the local proxies, which are loopback.
//
// An IPv6 address counts as its /64, the block one subscriber is given, so
// stepping through the addresses of one connection is not a new client each
// time.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := strings.TrimSpace(hops[i])
			if hop == "" {
				continue
			}
			if ip := net.ParseIP(hop); ip != nil && ip.IsLoopback() {
				continue
			}
			host = hop
			break
		}
	}
	return clientKey(host)
}

// clientKey reduces an IPv6 address to its /64.
func clientKey(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return host
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
