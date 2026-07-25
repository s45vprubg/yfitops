package transport

import (
	"sync"
	"testing"
)

// TestSessionLimiter_PerIPCap: a single IP is capped; a DIFFERENT IP is not
// starved by the first (the venue-NAT concern is per-IP, not global-only).
func TestSessionLimiter_PerIPCap(t *testing.T) {
	l := newSessionLimiter(0 /*unlimited total*/, 3)
	ip := "10.0.0.7"
	for i := 0; i < 3; i++ {
		if !l.acquire(ip) {
			t.Fatalf("acquire %d for %s should succeed (under per-IP cap)", i, ip)
		}
	}
	if l.acquire(ip) {
		t.Fatalf("4th acquire for %s should be REJECTED (per-IP cap=3)", ip)
	}
	// A different IP is unaffected.
	if !l.acquire("10.0.0.8") {
		t.Fatalf("a different IP must not be starved by another IP's cap")
	}
	// Releasing one frees a slot for the capped IP.
	l.release(ip)
	if !l.acquire(ip) {
		t.Fatalf("after release, %s should be admitted again", ip)
	}
}

// TestSessionLimiter_GlobalCap: the global ceiling rejects even across many IPs.
func TestSessionLimiter_GlobalCap(t *testing.T) {
	l := newSessionLimiter(2 /*total*/, 0 /*unlimited per-IP*/)
	if !l.acquire("a") || !l.acquire("b") {
		t.Fatalf("first two (distinct IPs) should be admitted")
	}
	if l.acquire("c") {
		t.Fatalf("third session should be REJECTED by the global cap=2")
	}
	l.release("a")
	if !l.acquire("c") {
		t.Fatalf("after a global release, a new session should be admitted")
	}
}

// TestSessionLimiter_ZeroDisables: 0 means unlimited for that dimension.
func TestSessionLimiter_ZeroDisables(t *testing.T) {
	l := newSessionLimiter(0, 0)
	for i := 0; i < 5000; i++ {
		if !l.acquire("same-ip") {
			t.Fatalf("with both caps disabled, acquire %d must succeed", i)
		}
	}
}

// TestSessionLimiter_ReleasePrunesAndFloorsAtZero: the per-IP map entry is
// removed at zero (no unbounded map growth) and counts never go negative.
func TestSessionLimiter_ReleasePrunesAndFloorsAtZero(t *testing.T) {
	l := newSessionLimiter(10, 10)
	l.acquire("x")
	l.release("x")
	l.mu.Lock()
	_, present := l.perIP["x"]
	total := l.total
	l.mu.Unlock()
	if present {
		t.Fatalf("perIP entry for x should be pruned at zero")
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	// Over-release must not underflow.
	l.release("x")
	l.mu.Lock()
	total = l.total
	l.mu.Unlock()
	if total != 0 {
		t.Fatalf("over-release drove total to %d, want 0 (no underflow)", total)
	}
}

// TestSessionLimiter_ConcurrentExact: under heavy concurrency the number of
// admitted sessions never exceeds the cap (the counts are correctly locked).
func TestSessionLimiter_ConcurrentExact(t *testing.T) {
	const cap = 50
	l := newSessionLimiter(cap, 0)
	var wg sync.WaitGroup
	var admitted int
	var mu sync.Mutex
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.acquire("ip") {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != cap {
		t.Fatalf("admitted %d concurrent sessions, want exactly %d (cap)", admitted, cap)
	}
}

// TestEnvInt: parsing/fallback, including the honored explicit 0.
func TestEnvInt(t *testing.T) {
	t.Setenv("YFI_TEST_LIMIT", "")
	if got := envInt("YFI_TEST_LIMIT", 42); got != 42 {
		t.Fatalf("blank -> default: got %d want 42", got)
	}
	t.Setenv("YFI_TEST_LIMIT", "0")
	if got := envInt("YFI_TEST_LIMIT", 42); got != 0 {
		t.Fatalf("explicit 0 must be honored (disables cap): got %d want 0", got)
	}
	t.Setenv("YFI_TEST_LIMIT", "garbage")
	if got := envInt("YFI_TEST_LIMIT", 42); got != 42 {
		t.Fatalf("invalid -> default: got %d want 42", got)
	}
	t.Setenv("YFI_TEST_LIMIT", "-5")
	if got := envInt("YFI_TEST_LIMIT", 42); got != 42 {
		t.Fatalf("negative -> default: got %d want 42", got)
	}
	t.Setenv("YFI_TEST_LIMIT", "256")
	if got := envInt("YFI_TEST_LIMIT", 42); got != 256 {
		t.Fatalf("valid -> parsed: got %d want 256", got)
	}
}
