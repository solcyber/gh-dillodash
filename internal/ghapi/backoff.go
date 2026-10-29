package ghapi

import (
	"sync"
	"time"
)

// Backoff is the refresh loop's answer to a failed call. A rate limit
// holds every refresh until the time GitHub named; any other failure
// retries after 15s, then 30s, 60s... capped at the refresh interval, so
// a dead network never pins the token at the wall. A clean call resets
// both.
type Backoff struct {
	// Refresh caps the exponential delay (the app's refresh interval).
	Refresh time.Duration

	mu          sync.Mutex
	streak      int
	until       time.Time // the next time a refresh may run
	rateLimited time.Time // GitHub's wall, zero when not rate-limited
}

// BackoffBase is the first retry delay; it doubles per consecutive
// failure up to 2^8 times.
const BackoffBase = 15 * time.Second

// Fail records a failed call at now and returns the time the next
// attempt may run: retryAt when GitHub named one (a rate limit), else
// 15s * 2^n capped at Refresh.
func (b *Backoff) Fail(now time.Time, retryAt time.Time) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !retryAt.IsZero() {
		b.rateLimited = retryAt
		b.until = retryAt
		return b.until
	}
	b.streak++
	n := b.streak - 1
	if n > 8 {
		n = 8
	}
	delay := BackoffBase << uint(n)
	if b.Refresh > 0 && delay > b.Refresh {
		delay = b.Refresh
	}
	b.until = now.Add(delay)
	return b.until
}

// OK records a clean call: the wall has lifted and the streak resets.
func (b *Backoff) OK() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.streak = 0
	b.until = time.Time{}
	b.rateLimited = time.Time{}
}

// RateLimit records a wall GitHub named on an otherwise clean call (a
// workflow hit it after the list landed): the streak resets, the wall
// stands.
func (b *Backoff) RateLimit(until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.streak = 0
	b.rateLimited = until
	b.until = until
}

// Until is the time the next attempt may run (zero when unconstrained).
func (b *Backoff) Until() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.until
}

// Wait is how long from now until the next attempt may run (0 when it
// may run now).
func (b *Backoff) Wait(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.until.IsZero() || !now.Before(b.until) {
		return 0
	}
	return b.until.Sub(now)
}

// RateLimitWait is how long GitHub's wall still stands (0 when it
// doesn't). A manual refresh honours only this: forcing a request through
// the wall re-trips it and extends the penalty, whereas an exponential
// backoff may be cut short by hand.
func (b *Backoff) RateLimitWait(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rateLimited.IsZero() || !now.Before(b.rateLimited) {
		return 0
	}
	return b.rateLimited.Sub(now)
}

// Streak is the number of consecutive non-rate-limit failures.
func (b *Backoff) Streak() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.streak
}
