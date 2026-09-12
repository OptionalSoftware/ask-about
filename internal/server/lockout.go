package server

import (
	"net/netip"
	"sync"
	"time"
)

// lockout counts failed admin logins per client address and refuses that
// address for a while once it has had too many.
//
// Per address rather than globally, so someone guessing cannot lock the owner
// out of their own admin pages. Held in memory: it resets on restart, which is
// the right trade for not writing to the database on every wrong password.
type lockout struct {
	max    int
	window time.Duration
	now    func() time.Time // swapped in tests

	mu      sync.Mutex
	tracked map[netip.Addr]*attempts
}

type attempts struct {
	count int
	// last is when the most recent failure happened. Entries older than the
	// window are dropped, which is also what stops the map growing without
	// bound when someone tries from many addresses.
	last time.Time
}

func newLockout(max int, window time.Duration) *lockout {
	return &lockout{
		max:     max,
		window:  window,
		now:     time.Now,
		tracked: make(map[netip.Addr]*attempts),
	}
}

// lockedOut reports whether this address has used up its attempts.
func (l *lockout) lockedOut(addr netip.Addr) bool {
	if l == nil || l.max <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.tracked[addr]
	if !ok {
		return false
	}
	if l.now().Sub(a.last) >= l.window {
		// The window has passed; forget it entirely rather than leaving a
		// stale count to be tripped over later.
		delete(l.tracked, addr)
		return false
	}
	return a.count >= l.max
}

// failed records a wrong password.
func (l *lockout) failed(addr netip.Addr) {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()

	a, ok := l.tracked[addr]
	if !ok || l.now().Sub(a.last) >= l.window {
		l.tracked[addr] = &attempts{count: 1, last: l.now()}
		return
	}
	a.count++
	a.last = l.now()
}

// succeeded clears the count, so a typo before the right password costs
// nothing.
func (l *lockout) succeeded(addr netip.Addr) {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tracked, addr)
}

// prune drops entries whose window has passed. Called on failure, which is the
// only path that grows the map.
func (l *lockout) prune() {
	cutoff := l.now().Add(-l.window)
	for addr, a := range l.tracked {
		if a.last.Before(cutoff) {
			delete(l.tracked, addr)
		}
	}
}
