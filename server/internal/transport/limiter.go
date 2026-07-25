package transport

import (
	"os"
	"strconv"
	"sync"
)

// Session limits (transport-2 follow-up, QA sweep 3). MaxIncomingStreams only
// bounds streams WITHIN a session, so a single host could still open unbounded
// QUIC sessions and exhaust goroutines/memory. sessionLimiter caps concurrent
// WebTransport sessions both globally and per client IP.
//
// Defaults are deliberately GENEROUS: this runs at a LAN party / conference
// where many legitimate players may share one NAT'd public IP, and reconnect
// churn briefly overlaps an old and new session for the same device. A cap that
// is too tight locks out real players — worse than the flood it prevents. The
// per-IP cap targets a single abusive source without touching a normal crowd;
// the global cap is the real resource ceiling. Both are env-tunable; 0 disables
// that dimension.
const (
	defaultMaxSessionsTotal = 2000 // global ceiling across all IPs
	defaultMaxSessionsPerIP = 128  // per-source ceiling (generous for shared NAT)
)

// sessionLimiter tracks live session counts and admits or rejects a new one.
// The zero value is not usable; construct with newSessionLimiter.
type sessionLimiter struct {
	mu       sync.Mutex
	total    int
	perIP    map[string]int
	maxTotal int // <=0 means unlimited
	maxPerIP int // <=0 means unlimited
}

func newSessionLimiter(maxTotal, maxPerIP int) *sessionLimiter {
	return &sessionLimiter{
		perIP:    make(map[string]int),
		maxTotal: maxTotal,
		maxPerIP: maxPerIP,
	}
}

// newSessionLimiterFromEnv builds a limiter from YFI_MAX_SESSIONS /
// YFI_MAX_SESSIONS_PER_IP, falling back to the generous defaults. A value of 0
// disables that dimension (explicit opt-out for an operator who wants no cap).
func newSessionLimiterFromEnv() *sessionLimiter {
	return newSessionLimiter(
		envInt("YFI_MAX_SESSIONS", defaultMaxSessionsTotal),
		envInt("YFI_MAX_SESSIONS_PER_IP", defaultMaxSessionsPerIP),
	)
}

// acquire admits a session from ip. It returns true and increments the counts
// if both the global and per-IP ceilings allow it; otherwise it returns false
// and changes nothing. release must be called exactly once for every acquire
// that returned true.
func (l *sessionLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxTotal > 0 && l.total >= l.maxTotal {
		return false
	}
	if l.maxPerIP > 0 && l.perIP[ip] >= l.maxPerIP {
		return false
	}
	l.total++
	l.perIP[ip]++
	return true
}

// release returns a session's slot. It is safe to call only after a matching
// acquire returned true. It never lets a count go negative and prunes the map
// entry when an IP drops to zero so the map cannot grow unbounded.
func (l *sessionLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total > 0 {
		l.total--
	}
	if n := l.perIP[ip]; n > 1 {
		l.perIP[ip] = n - 1
	} else if n == 1 {
		delete(l.perIP, ip)
	}
}

// envInt reads a non-negative int from the environment, falling back to def on
// an unset/blank/invalid value. A parsed 0 is honored (disables the dimension).
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
