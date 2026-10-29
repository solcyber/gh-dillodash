package ghapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Error is a failed GitHub call. Status is 0 for anything that never got
// an HTTP status (a network error, a truncated body, a GraphQL error in
// a 200); RetryAt is set when GitHub asked us to back off.
type Error struct {
	Method  string
	URL     string
	Status  int
	Message string
	RetryAt time.Time
}

// Error is the short, user-facing form: "GitHub 502 Bad Gateway" when
// the message is just the status text, "GitHub 401: Bad credentials"
// when GitHub said more, the message alone when there was no status.
// Verbose adds the method and URL.
func (e *Error) Error() string {
	if e.Status == 0 {
		return e.Message
	}
	text := http.StatusText(e.Status)
	msg := strings.TrimSpace(strings.TrimPrefix(e.Message, strconv.Itoa(e.Status)))
	if msg == "" || strings.EqualFold(msg, text) {
		return fmt.Sprintf("GitHub %d %s", e.Status, text)
	}
	return fmt.Sprintf("GitHub %d: %s", e.Status, msg)
}

// Verbose is the long form with the request: "POST https://... -> 502:
// Bad Gateway", for the doctor and logs.
func (e *Error) Verbose() string {
	if e.Status == 0 {
		return e.Message
	}
	return fmt.Sprintf("%s %s -> %d: %s", e.Method, e.URL, e.Status, e.Message)
}

// IsRateLimit reports whether GitHub told us when to retry.
func (e *Error) IsRateLimit() bool { return !e.RetryAt.IsZero() }

// AsError unwraps err to the *Error it carries, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsRateLimit reports whether err is a rate-limit *Error.
func IsRateLimit(err error) bool {
	e, ok := AsError(err)
	return ok && e.IsRateLimit()
}

// isNetwork reports whether err is a "network error: ..." *Error.
func isNetwork(err error) bool {
	e, ok := AsError(err)
	return ok && strings.HasPrefix(e.Message, "network error")
}

// secondaryFloor is the least GitHub asks a client to wait after a
// secondary (abuse) rate limit when no Retry-After says otherwise;
// retrying sooner lengthens the penalty.
const secondaryFloor = 60 * time.Second

// retryAtFromHeaders is the time to back off until, from a GitHub
// response's rate-limit headers; ok is false if the headers don't
// indicate a rate limit.
//
// Secondary limits send Retry-After (delta seconds, or an HTTP date);
// primary limits send X-RateLimit-Remaining: 0 with X-RateLimit-Reset
// (absolute epoch).
func retryAtFromHeaders(h http.Header, now time.Time) (time.Time, bool) {
	if h == nil {
		return time.Time{}, false
	}
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
			return now.Add(time.Duration(n) * time.Second), true
		}
		if t, err := http.ParseTime(ra); err == nil {
			return t, true
		}
	}
	if strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" {
		if reset := strings.TrimSpace(h.Get("X-RateLimit-Reset")); reset != "" {
			if n, err := strconv.ParseInt(reset, 10, 64); err == nil && n >= 0 {
				return time.Unix(n, 0), true
			}
		}
	}
	return time.Time{}, false
}

// isSecondaryLimit recognises a secondary (abuse) rate limit by its
// shape rather than its headers: any 429, or a 403 whose message says
// so. Those often arrive without Retry-After.
func isSecondaryLimit(status int, msg string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	m := strings.ToLower(msg)
	return strings.Contains(m, "secondary rate limit") || strings.Contains(m, "abuse")
}

// retryAt decides whether a response is a rate limit and until when:
// the headers when they say, else the secondary floor for a response
// shaped like a secondary limit. Only 403 and 429 qualify.
func retryAt(status int, msg string, h http.Header, now time.Time) (time.Time, bool) {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return time.Time{}, false
	}
	if at, ok := retryAtFromHeaders(h, now); ok {
		return at, true
	}
	if isSecondaryLimit(status, msg) {
		return now.Add(secondaryFloor), true
	}
	return time.Time{}, false
}
