package auth

import (
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// limiter counts failures per key and refuses a key once it has max of them
// within window. A success does not wipe the count: someone who signs in to
// their own account must not earn more guesses at another one.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
	swept  time.Time // when stale keys were last dropped
}

// limiterKeys bounds the keys a limiter remembers under a flood of addresses.
const limiterKeys = 100_000

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

// wait is how long key stays refused, or 0 if it may try now.
func (l *limiter) wait(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.recent(key, now)
	if len(kept) < l.max {
		return 0
	}
	return kept[len(kept)-l.max].Add(l.window).Sub(now)
}

// fail records a failure for key.
func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key, now), now)
	if len(l.fails) > limiterKeys/10 && now.Sub(l.swept) > time.Minute {
		l.swept = now
		cut := now.Add(-l.window)
		for k, v := range l.fails {
			if v[len(v)-1].Before(cut) {
				delete(l.fails, k)
			}
		}
	}
	// Over the bound even so: forget keys at random (map order is). Each one
	// forgotten starts again from no failures, which only a flood can cause.
	for k := range l.fails {
		if len(l.fails) <= limiterKeys {
			break
		}
		delete(l.fails, k)
	}
}

// recent drops key's failures older than the window and returns the rest.
// The caller holds mu.
func (l *limiter) recent(key string, now time.Time) []time.Time {
	cut := now.Add(-l.window)
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

// ipKey groups addresses the way one sender holds them: IPv6 by /64, the
// smallest block a home connection or a server is given.
func ipKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	p, _ := addr.Prefix(64)
	return p.String()
}

// TooManyAttemptsError refuses a sign-in for a while after too many failed ones.
type TooManyAttemptsError struct {
	Wait time.Duration
}

// Error says how long to wait, in minutes.
func (e *TooManyAttemptsError) Error() string {
	if m := int(e.Wait.Round(time.Minute) / time.Minute); m > 1 {
		return fmt.Sprintf("too many failed attempts; try again in %d minutes", m)
	}
	return "too many failed attempts; try again in a minute"
}
