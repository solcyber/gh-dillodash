package ghapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
)

// Usage counts what the dashboard asks GitHub for, by who asked: the
// refresh, a workflow's poll, a detail read, an action. It reads the
// budget GitHub reports on every response (REST and GraphQL alike send
// X-RateLimit-* headers, a 304 included), so the header can show the
// tighter of the two budgets and the cadence can slow down before a
// wall, not after it. One Usage is shared by every client of a process.
type Usage struct {
	mu      sync.Mutex
	start   time.Time
	by      map[string]*Count
	budgets map[string]Budget // by X-RateLimit-Resource: core, graphql, search
	trace   io.Writer
	enc     *json.Encoder
}

// Count is one source's requests since the process started.
type Count struct {
	REST    int // answered 2xx
	REST304 int // answered 304 Not Modified: free against the primary limit
	RESTErr int // any other answer, or no answer
	GQL     int // GraphQL queries and mutations
	GQLErr  int
	GQLCost int // points GraphQL charged (rateLimit.cost)
}

// Calls is every request the source made.
func (c Count) Calls() int { return c.REST + c.REST304 + c.RESTErr + c.GQL + c.GQLErr }

// Budget is one rate-limit resource as GitHub last reported it.
type Budget struct {
	Resource  string
	Limit     int
	Remaining int
	Used      int
	Reset     time.Time
	At        time.Time // when it was read
}

// Fraction is how much of the budget is left (1 when unknown).
func (b Budget) Fraction() float64 {
	if b.Limit <= 0 {
		return 1
	}
	return float64(b.Remaining) / float64(b.Limit)
}

// NewUsage starts a count at now.
func NewUsage() *Usage {
	return &Usage{start: clock.Now(), by: map[string]*Count{}, budgets: map[string]Budget{}}
}

// SetTrace writes one JSON line per request to w (nil stops it): the
// time, the source, the method, the path with its owner, repo and
// numbers folded away, the status and, for GraphQL, the points charged.
func (u *Usage) SetTrace(w io.Writer) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.trace = w
	u.enc = nil
	if w != nil {
		u.enc = json.NewEncoder(w)
	}
}

// sourceKey is the context key of a request's source.
type sourceKey struct{}

// WithSource tags every request made under ctx with src ("refresh",
// "poll:waiting_runs", "detail", ...). A tag already on ctx is kept as
// the prefix: "refresh" then "release_pr" reads "refresh:release_pr".
func WithSource(ctx context.Context, src string) context.Context {
	if prev := SourceOf(ctx); prev != "" && prev != SourceAction {
		src = prev + ":" + src
	}
	return context.WithValue(ctx, sourceKey{}, src)
}

// SourceAction is the source of an untagged request: what the user
// asked for by a key.
const SourceAction = "action"

// SourceOf is ctx's source, SourceAction when untagged.
func SourceOf(ctx context.Context) string {
	if ctx != nil {
		if s, ok := ctx.Value(sourceKey{}).(string); ok && s != "" {
			return s
		}
	}
	return SourceAction
}

func (u *Usage) count(src string) *Count {
	c := u.by[src]
	if c == nil {
		c = &Count{}
		u.by[src] = c
	}
	return c
}

// record notes one answered (or failed) request; gql marks a GraphQL
// post. h may be nil (no answer).
func (u *Usage) record(ctx context.Context, method, rawURL string, status int, h http.Header, gql bool) {
	if u == nil {
		return
	}
	src := SourceOf(ctx)
	now := clock.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	c := u.count(src)
	switch {
	case gql && status >= 200 && status < 300:
		c.GQL++
	case gql:
		c.GQLErr++
	case status == http.StatusNotModified:
		c.REST304++
	case status >= 200 && status < 300:
		c.REST++
	default:
		c.RESTErr++
	}
	if b, ok := budgetOf(h, now); ok {
		// answers can land out of order: keep the newest window's lowest
		// remaining, never step back to an older read
		if prev, had := u.budgets[b.Resource]; !had || b.Reset.After(prev.Reset) || (b.Reset.Equal(prev.Reset) && b.Remaining <= prev.Remaining) {
			u.budgets[b.Resource] = b
		}
	}
	if u.enc != nil && !gql {
		_ = u.enc.Encode(traceLine{At: now.UTC().Format(time.RFC3339Nano), Source: src, Method: method, Path: PathClass(rawURL), Status: status})
	}
}

// recordCost adds the points one GraphQL answer charged, and traces it
// (the GraphQL trace line waits for the cost, which is in the body).
func (u *Usage) recordCost(ctx context.Context, query string, cost int) {
	if u == nil {
		return
	}
	src := SourceOf(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.count(src).GQLCost += cost
	if u.enc != nil {
		_ = u.enc.Encode(traceLine{At: clock.Now().UTC().Format(time.RFC3339Nano), Source: src, Method: "POST", Path: "graphql " + QueryClass(query), Status: 200, Cost: cost})
	}
}

type traceLine struct {
	At     string `json:"at"`
	Source string `json:"source"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Cost   int    `json:"cost,omitempty"`
}

// budgetOf reads the X-RateLimit-* headers of one answer.
func budgetOf(h http.Header, now time.Time) (Budget, bool) {
	if h == nil {
		return Budget{}, false
	}
	limit, err1 := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Limit")))
	rem, err2 := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Remaining")))
	if err1 != nil || err2 != nil || limit <= 0 {
		return Budget{}, false
	}
	b := Budget{Resource: strings.TrimSpace(h.Get("X-RateLimit-Resource")), Limit: limit, Remaining: rem, At: now}
	if b.Resource == "" {
		b.Resource = "core"
	}
	b.Used, _ = strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Used")))
	if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && reset > 0 {
		b.Reset = time.Unix(reset, 0)
	}
	return b, true
}

// UsageSnapshot is a copy of the counts for drawing and reports.
type UsageSnapshot struct {
	Since   time.Time
	By      map[string]Count
	Budgets map[string]Budget
}

// Snapshot copies the counts (a nil Usage gives an empty one).
func (u *Usage) Snapshot() UsageSnapshot {
	s := UsageSnapshot{By: map[string]Count{}, Budgets: map[string]Budget{}}
	if u == nil {
		return s
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s.Since = u.start
	for k, v := range u.by {
		s.By[k] = *v
	}
	for k, v := range u.budgets {
		s.Budgets[k] = v
	}
	return s
}

// Total sums every source.
func (s UsageSnapshot) Total() Count {
	var t Count
	for _, c := range s.By {
		t.REST += c.REST
		t.REST304 += c.REST304
		t.RESTErr += c.RESTErr
		t.GQL += c.GQL
		t.GQLErr += c.GQLErr
		t.GQLCost += c.GQLCost
	}
	return t
}

// Sources are the sources, most calls first.
func (s UsageSnapshot) Sources() []string {
	out := make([]string, 0, len(s.By))
	for k := range s.By {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := s.By[out[i]], s.By[out[j]]
		if a.GQLCost != b.GQLCost {
			return a.GQLCost > b.GQLCost
		}
		if a.Calls() != b.Calls() {
			return a.Calls() > b.Calls()
		}
		return out[i] < out[j]
	})
	return out
}

// Tightest is the budget with the least left, read within the last
// hour (an older read says nothing about now): GraphQL or core.
func (s UsageSnapshot) Tightest(now time.Time) (Budget, bool) {
	var best Budget
	found := false
	for _, r := range []string{"graphql", "core"} {
		b, ok := s.Budgets[r]
		if !ok || now.Sub(b.At) > time.Hour {
			continue
		}
		if !b.Reset.IsZero() && now.After(b.Reset) {
			continue // the window reset since: it is full again
		}
		if !found || b.Fraction() < best.Fraction() {
			best, found = b, true
		}
	}
	return best, found
}

var (
	repoPathRE = regexp.MustCompile(`^repos/[^/]+/[^/]+`)
	numberRE   = regexp.MustCompile(`/[0-9a-f]{40}|/\d+`)
	shaParamRE = regexp.MustCompile(`=[0-9a-f]{7,40}\b`)
)

// PathClass folds a REST URL to its shape: no host, owner, repo,
// numbers or SHAs, so a trace says which endpoints are read and how
// often without naming what was read ("repos/R/actions/runs?status=waiting").
func PathClass(rawURL string) string {
	p := rawURL
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
		if j := strings.IndexByte(p, '/'); j >= 0 {
			p = p[j+1:]
		} else {
			p = ""
		}
	}
	p = strings.TrimPrefix(p, "api/v3/")
	p = repoPathRE.ReplaceAllString(p, "repos/R")
	if strings.HasPrefix(p, "orgs/") {
		if parts := strings.SplitN(p, "/", 3); len(parts) >= 2 {
			parts[1] = "O"
			p = strings.Join(parts, "/")
		}
	}
	p = numberRE.ReplaceAllString(p, "/N")
	p = shaParamRE.ReplaceAllString(p, "=SHA")
	return p
}

var queryFieldRE = regexp.MustCompile(`(?:^|[\s{])([A-Za-z_]\w*)\s*(?:\(|:|\{)`)

// QueryClass names a GraphQL document by its first field past the
// rateLimit block ("repository", "search", "r0: repository" reads as
// "r0"), enough to tell the list from a detail read in a trace.
func QueryClass(query string) string {
	q := query
	if i := strings.Index(q, "rateLimit"); i >= 0 {
		if j := strings.Index(q[i:], "}"); j >= 0 {
			q = q[i+j+1:]
		}
	} else if i := strings.IndexByte(q, '{'); i >= 0 {
		q = q[i+1:]
	}
	if m := queryFieldRE.FindStringSubmatch(q); m != nil {
		return m[1]
	}
	return "query"
}
