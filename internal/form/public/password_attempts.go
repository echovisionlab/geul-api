package public

import (
	"sync"
	"time"
)

const (
	formPasswordAttemptLimit  = 10
	formPasswordAttemptWindow = 15 * time.Minute
	formPasswordAttemptKeys   = 10_000
)

// passwordAttempts bounds failed Form password guesses per Form and client.
// Form passwords are shared access codes, often short, so an unbounded oracle
// would let a client enumerate them. The budget is process-local: the API
// runs as one replica, and a restart only grants a fresh window.
type passwordAttempts struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[string]passwordAttemptWindow
}

type passwordAttemptWindow struct {
	failures int
	start    time.Time
}

func newPasswordAttempts(now func() time.Time) *passwordAttempts {
	return &passwordAttempts{now: now, windows: make(map[string]passwordAttemptWindow)}
}

func passwordAttemptKey(formID, client string) string {
	return formID + "\x00" + client
}

// exhausted reports whether key has used its failed-attempt budget.
func (a *passwordAttempts) exhausted(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	window, ok := a.windows[key]
	if !ok {
		return false
	}
	if a.now().Sub(window.start) >= formPasswordAttemptWindow {
		delete(a.windows, key)
		return false
	}
	return window.failures >= formPasswordAttemptLimit
}

// record counts a failed guess; a correct password clears the window.
func (a *passwordAttempts) record(key string, matched bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if matched {
		delete(a.windows, key)
		return
	}
	now := a.now()
	window, ok := a.windows[key]
	if !ok || now.Sub(window.start) >= formPasswordAttemptWindow {
		if len(a.windows) >= formPasswordAttemptKeys {
			a.pruneLocked(now)
		}
		window = passwordAttemptWindow{start: now}
	}
	window.failures++
	a.windows[key] = window
}

func (a *passwordAttempts) pruneLocked(now time.Time) {
	for key, window := range a.windows {
		if now.Sub(window.start) >= formPasswordAttemptWindow {
			delete(a.windows, key)
		}
	}
}
