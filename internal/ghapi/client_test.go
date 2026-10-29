package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// gqlReq is what the test server sees of a GraphQL post.
type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// server is an httptest.Server with a per-path handler table; every
// request is recorded.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	handlers map[string]http.HandlerFunc // "METHOD /path" -> handler
	graphql  func(w http.ResponseWriter, req gqlReq)
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{handlers: map[string]http.HandlerFunc{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// the handlers are read under the lock: a test swaps them between
		// calls while the server goroutine of the previous request may
		// still be winding down
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, string(body))
		graphql := s.graphql
		h, ok := s.handlers[r.Method+" "+r.URL.Path]
		s.mu.Unlock()
		if r.URL.Path == "/graphql" && graphql != nil {
			var q gqlReq
			_ = json.Unmarshal(body, &q)
			graphql(w, q)
			return
		}
		if ok {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"message":"Not Found: %s %s"}`, r.Method, r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) handle(method, path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method+" "+path] = h
}

// setGraphQL swaps the GraphQL handler (a test changes answers between
// calls).
func (s *server) setGraphQL(h func(w http.ResponseWriter, req gqlReq)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphql = h
}

func (s *server) jsonOK(method, path string, body string) {
	s.handle(method, path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
}

func (s *server) client(t *testing.T) *GitHub {
	t.Helper()
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(s.URL, "http://"))
	c, err := New(Options{
		// Host is the test server's hostname so go-gh's header round
		// tripper attaches the token (it only does so on Host's domain).
		Host:    host,
		Token:   "tok-alice",
		Version: "test",
		BaseURL: s.URL,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *server) last() (*http.Request, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil, ""
	}
	return s.requests[len(s.requests)-1], s.bodies[len(s.bodies)-1]
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func rateLimitJSON() string {
	return `"rateLimit":{"limit":5000,"remaining":4321,"resetAt":"2026-10-28T13:00:00Z"}`
}

// ---- REST transport ------------------------------------------------------

func TestWhoamiAndHeaders(t *testing.T) {
	s := newServer(t)
	s.jsonOK("GET", "/user", `{"login":"alice"}`)
	c := s.client(t)
	login, err := c.Whoami(context.Background())
	if err != nil || login != "alice" {
		t.Fatalf("whoami: %q %v", login, err)
	}
	req, _ := s.last()
	if got := req.Header.Get("Authorization"); got != "token tok-alice" {
		t.Errorf("Authorization: %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "dillodash/test" {
		t.Errorf("User-Agent: %q", got)
	}
	if got := req.Header.Get("Accept"); got != acceptJSON {
		t.Errorf("Accept: %q", got)
	}
	if c.TokenSource() != "option" || c.Host() == "" {
		t.Errorf("token source %q host %q", c.TokenSource(), c.Host())
	}
}

func TestNewNeedsAToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
	if _, err := New(Options{Host: "ghe.nowhere.test"}); err == nil {
		t.Error("no token anywhere should fail New")
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", "tok-env")
	c, err := New(Options{Host: "ghe.nowhere.test"})
	if err != nil || c.TokenSource() != "GH_ENTERPRISE_TOKEN" {
		t.Errorf("env token: %v source %q", err, c.TokenSource())
	}
}

func TestHTTPErrorsAndRateLimitHeaders(t *testing.T) {
	s := newServer(t)
	s.handle("POST", "/repos/acme/infra/pulls/3543/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		io.WriteString(w, `{"message":"boom"}`)
	})
	s.handle("GET", "/repos/acme/infra/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(403)
		io.WriteString(w, `{"message":"You have exceeded a secondary rate limit"}`)
	})
	s.handle("GET", "/repos/acme/infra/rulesets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1800000000")
		w.WriteHeader(429)
		io.WriteString(w, `{"message":"API rate limit exceeded for user ID 1"}`)
	})
	s.handle("GET", "/repos/acme/infra/pulls/1/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
	})
	s.handle("GET", "/repos/acme/infra/git/matching-refs/tags/go/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(502)
		io.WriteString(w, `<html>Bad Gateway</html>`)
	})
	c := s.client(t)
	ctx := context.Background()

	err := c.Approve(ctx, infra, 3543, "lgtm")
	e, ok := AsError(err)
	if !ok || e.Status != 500 || e.Message != "boom" || e.IsRateLimit() {
		t.Fatalf("500: %#v", err)
	}
	if e.Error() != "GitHub 500: boom" {
		t.Errorf("Error(): %q", e.Error())
	}
	if !strings.HasPrefix(e.Verbose(), "POST http://") || !strings.HasSuffix(e.Verbose(), "/repos/acme/infra/pulls/3543/reviews -> 500: boom") {
		t.Errorf("Verbose(): %q", e.Verbose())
	}

	before := time.Now()
	_, err = c.WorkflowRuns(ctx, infra, "abc")
	e, _ = AsError(err)
	if e == nil || e.Status != 403 || !e.IsRateLimit() {
		t.Fatalf("secondary limit: %#v", err)
	}
	if d := e.RetryAt.Sub(before); d < 40*time.Second || d > 44*time.Second {
		t.Errorf("Retry-After -> now+42s, got %v", d)
	}

	_, err = c.Rulesets(ctx, infra)
	e, _ = AsError(err)
	if e == nil || e.Status != 429 || !e.IsRateLimit() || e.RetryAt.Unix() != 1800000000 {
		t.Fatalf("primary limit: %#v", err)
	}

	// a 403 without rate-limit headers is a permission problem, not a wall
	_, err = c.PRFiles(ctx, infra, 1)
	e, _ = AsError(err)
	if e == nil || e.Status != 403 || e.IsRateLimit() || !strings.Contains(e.Message, "not accessible") {
		t.Fatalf("plain 403: %#v", err)
	}

	// a non-JSON error body keeps the HTTP status text
	_, err = c.TagRefs(ctx, infra, "go/")
	e, _ = AsError(err)
	if e == nil || e.Status != 502 || !strings.Contains(e.Message, "Bad Gateway") {
		t.Fatalf("html 502: %#v", err)
	}
}

func TestNetworkErrors(t *testing.T) {
	// a closed port: connection refused
	s := newServer(t)
	c := s.client(t)
	s.Close()
	_, err := c.Whoami(context.Background())
	if !isNetwork(err) {
		t.Fatalf("closed server: %v", err)
	}
	if e, _ := AsError(err); e.Status != 0 || !strings.HasPrefix(e.Error(), "network error: ") {
		t.Errorf("shape: %q", err.Error())
	}

	// a hung server: timed out
	slow := newServer(t)
	slow.handle("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(slow.URL, "http://"))
	fast, err := New(Options{Host: host, Token: "t", BaseURL: slow.URL, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fast.Whoami(context.Background())
	if !isNetwork(err) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}

	// a dropped connection mid-body
	drop := newServer(t)
	drop.handle("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, `{"login":"ali`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			conn.Close()
		}
	})
	_, err = drop.client(t).Whoami(context.Background())
	if !isNetwork(err) {
		t.Fatalf("dropped mid-body: %v", err)
	}

	// a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.client(t).Whoami(ctx)
	if !isNetwork(err) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestTruncatedBody(t *testing.T) {
	s := newServer(t)
	s.jsonOK("GET", "/user", `{"trunc`)
	_, err := s.client(t).Whoami(context.Background())
	e, ok := AsError(err)
	if !ok || !strings.HasPrefix(e.Message, "bad response from GET ") || isNetwork(err) {
		t.Fatalf("truncated JSON: %v", err)
	}
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data": {"repository": {`)
	})
	_, err = s.client(t).PRDetail(context.Background(), infra, 1)
	if e, ok := AsError(err); !ok || !strings.HasPrefix(e.Message, "bad response from POST ") {
		t.Fatalf("truncated GraphQL: %v", err)
	}
}

// ---- GraphQL --------------------------------------------------------------

func TestGraphQLRateLimitedIn200(t *testing.T) {
	s := newServer(t)
	// GraphQL rate limiting arrives as HTTP 200 + a RATE_LIMITED error, so it
	// never hits the HTTP-error path — it must still tag a backoff.
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data": null, "errors": [{"type": "RATE_LIMITED", "message": "API rate limit exceeded for user ID 1"}]}`)
	})
	c := s.client(t)
	before := time.Now()
	_, err := c.PRDetail(context.Background(), infra, 3543)
	e, ok := AsError(err)
	if !ok || !e.IsRateLimit() || e.Message != "API rate limit exceeded for user ID 1" {
		t.Fatalf("RATE_LIMITED: %#v", err)
	}
	// no headers on the response -> the +60s fallback applies
	if d := e.RetryAt.Sub(before); d < 50*time.Second || d > 61*time.Second {
		t.Errorf("fallback backoff: %v", d)
	}
	// with headers, the reset wins
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1900000000")
		io.WriteString(w, `{"data": null, "errors": [{"message": "you hit the rate limit, friend"}]}`)
	})
	_, err = c.PRDetail(context.Background(), infra, 3543)
	e, _ = AsError(err)
	if e == nil || !e.IsRateLimit() || e.RetryAt.Unix() != 1900000000 {
		t.Fatalf("rate limit by message + headers: %#v", err)
	}
	// ListOpen and ReleasePRs surface it as the call's error
	_, err = c.ListOpen(context.Background(), []model.Repo{infra})
	if !IsRateLimit(err) {
		t.Errorf("ListOpen rate limit: %v", err)
	}
	_, err = c.ReleasePRs(context.Background(), []model.Repo{infra, portal}, "main", "stage")
	if !IsRateLimit(err) {
		t.Errorf("ReleasePRs rate limit: %v", err)
	}
	// a plain GraphQL failure is not a rate limit
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors": [{"message": "Something went wrong"}]}`)
	})
	_, err = c.AssignableUsers(context.Background(), infra)
	e, _ = AsError(err)
	if e == nil || e.IsRateLimit() || e.Message != "Something went wrong" {
		t.Fatalf("plain GraphQL error: %#v", err)
	}
}

func listPR(n int, title string) string {
	return fmt.Sprintf(`{"number":%d,"title":%q,"isDraft":false,"reviewDecision":"REVIEW_REQUIRED",
		"updatedAt":"2026-10-21T10:00:00Z","createdAt":"2026-10-20T10:00:00Z","headRefName":"feat/%d","headRefOid":"h%d",
		"additions":1,"deletions":2,"author":{"login":"bob"},"labels":{"nodes":[{"name":"window:queued"}]},
		"latestOpinionatedReviews":{"nodes":[{"author":{"login":"alice"},"state":"APPROVED"}]},
		"commits":{"nodes":[{"commit":{"committedDate":"2026-10-21T09:00:00Z","statusCheckRollup":{"state":"SUCCESS",
		"contexts":{"totalCount":2,"nodes":[{"__typename":"CheckRun","name":"plan","status":"COMPLETED","conclusion":"SUCCESS"},
		{"__typename":"StatusContext","context":"gate","state":"PENDING","description":"waiting"}]}}}}]}}`, n, title, n, n)
}

func TestListOpenAliased(t *testing.T) {
	s := newServer(t)
	var queries []gqlReq
	var qmu sync.Mutex
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		qmu.Lock()
		queries = append(queries, req)
		qmu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		n := 0
		for k := range req.Variables {
			if strings.HasPrefix(k, "n") {
				n++
			}
		}
		if n == 1 { // the second chunk: one repo, not visible
			io.WriteString(w, `{"data":{`+rateLimitJSON()+`,"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"Could not resolve to a Repository with the name 'acme/ghost'."}]}`)
			return
		}
		io.WriteString(w, `{"data":{`+rateLimitJSON()+`,
			"r0":{"viewerPermission":"ADMIN","issues":{"totalCount":1,"nodes":[{"number":3530,"title":"an issue","updatedAt":"2026-10-20T10:00:00Z","createdAt":"2026-10-19T10:00:00Z",
				"author":{"login":"infra-bot"},"labels":{"nodes":[{"name":"window-manifest"}]},"assignees":{"nodes":[{"login":"carol"}]},"comments":{"totalCount":12}}]},
				"pullRequests":{"totalCount":2,"nodes":[`+listPR(3543, "canary")+`,`+listPR(3550, "widen the role")+`]}},
			"r1":{"issues":null,"pullRequests":{"totalCount":150,"nodes":[`+listPR(1042, "Release 2026-10-27")+`]}},
			"r2":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[null]}},
			"r3":null},
			"errors":[{"type":"FORBIDDEN","path":["r1","issues"],"message":"Resource not accessible by personal access token"},
			          {"type":"NOT_FOUND","path":["r3"],"message":"Could not resolve to a Repository with the name 'acme/typo'."}]}`)
	})
	c := s.client(t)
	docs := model.Repo{Owner: "acme", Name: "docs"}
	typo := model.Repo{Owner: "acme", Name: "typo"}
	ghost := model.Repo{Owner: "acme", Name: "ghost"}
	repos := []model.Repo{infra, portal, docs, typo, ghost}
	out, err := c.ListOpen(context.Background(), repos)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("one request per chunk of 4: %d", len(queries))
	}
	// the aliased request carries every repo as its own variables
	qmu.Lock()
	var big gqlReq
	for _, q := range queries {
		if len(q.Variables) == 8 {
			big = q
		}
	}
	qmu.Unlock()
	if big.Variables["o0"] != "acme" || big.Variables["n0"] != "infra" || big.Variables["n3"] != "typo" {
		t.Errorf("variables: %v", big.Variables)
	}
	if !strings.Contains(big.Query, "r3: repository(owner: $o3, name: $n3) { ...ListRepo }") {
		t.Errorf("query: %s", big.Query)
	}

	r := out[infra]
	if r.Err != nil || len(r.PRs) != 2 || len(r.Issues) != 1 || len(r.Notes) != 0 || r.Permission != "ADMIN" {
		t.Fatalf("infra: %+v", r)
	}
	if r.PRs[0].Repo != infra || r.PRs[0].Number != 3543 || r.PRs[0].Labels.Names()[0] != "window:queued" ||
		r.PRs[0].MyReviewState("alice") != "APPROVED" || r.PRs[0].HeadRefOid != "h3543" {
		t.Errorf("pr parse: %+v", r.PRs[0])
	}
	if cl, ok := model.ClusterCI(r.PRs[0]); !ok || cl.Glyphs != "+." {
		t.Errorf("rollup parse: %+v %v", cl, ok)
	}
	if r.Issues[0].Repo != infra || r.Issues[0].Author.Login != "infra-bot" || r.Issues[0].Assignees.Nodes[0].Login != "carol" ||
		r.Issues[0].Comments.TotalCount != 12 || !r.Issues[0].Labels.Has("window-manifest") {
		t.Errorf("issue parse: %+v", r.Issues[0])
	}

	r = out[portal]
	if r.Err != nil || len(r.PRs) != 1 || len(r.Issues) != 0 || r.Permission != "" {
		t.Fatalf("web-portal: %+v", r)
	}
	if len(r.Notes) != 2 || r.Notes[0] != "issues hidden — token needs Issues: read (Resource not accessible by personal access token)" ||
		r.Notes[1] != "showing the newest 1 of 150 open PRs" {
		t.Errorf("web-portal notes: %q", r.Notes)
	}

	if r = out[docs]; r.Err != nil || len(r.PRs) != 0 || len(r.Notes) != 0 {
		t.Errorf("docs (a null PR node is skipped): %+v", r)
	}

	if r = out[typo]; r.Err == nil || !strings.Contains(r.Err.Error(), "repository not visible to this token: Could not resolve to a Repository with the name 'acme/typo'.") {
		t.Errorf("typo: %v", r.Err)
	}
	if r = out[ghost]; r.Err == nil || !strings.Contains(r.Err.Error(), "acme/ghost") {
		t.Errorf("ghost: %v", r.Err)
	}
	rl := c.LastRate()
	if rl == nil || rl.Remaining != 4321 || rl.Limit != 5000 {
		t.Errorf("LastRate: %+v", rl)
	}
	// an empty repo list makes no request
	n := s.count()
	if out, err := c.ListOpen(context.Background(), nil); err != nil || len(out) != 0 || s.count() != n {
		t.Error("empty list should not call")
	}
}

func TestListOpenChunkFailure(t *testing.T) {
	s := newServer(t)
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		if len(req.Variables) == 2 { // the lone fifth repo's chunk
			w.WriteHeader(502)
			io.WriteString(w, `{"message":"Bad Gateway"}`)
			return
		}
		io.WriteString(w, `{"data":{"r0":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}},
			"r1":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}},
			"r2":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}},
			"r3":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}}}}`)
	})
	repos := []model.Repo{infra, portal, {Owner: "acme", Name: "a"}, {Owner: "acme", Name: "b"}, {Owner: "acme", Name: "lonely"}}
	out, err := s.client(t).ListOpen(context.Background(), repos)
	if err != nil {
		t.Fatalf("one failed chunk is not the call's error: %v", err)
	}
	if out[infra].Err != nil || out[repos[4]].Err == nil || !strings.Contains(out[repos[4]].Err.Error(), "502") {
		t.Errorf("the failed chunk's repos carry the error, the others landed: %+v / %+v", out[infra], out[repos[4]])
	}
}

func TestListOpenAllChunksFailIsAnError(t *testing.T) {
	s := newServer(t)
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		io.WriteString(w, `{"message":"Bad Gateway"}`)
	})
	repos := []model.Repo{infra, portal, {Owner: "acme", Name: "a"}, {Owner: "acme", Name: "b"}, {Owner: "acme", Name: "lonely"}}
	out, err := s.client(t).ListOpen(context.Background(), repos)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("every chunk failing is an outage, so the call fails: %v", err)
	}
	if out[infra].Err == nil || out[repos[4]].Err == nil {
		t.Error("every repo carries the error too")
	}
}

func TestListOpenRateLimitFailsTheCall(t *testing.T) {
	s := newServer(t)
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		if len(req.Variables) == 2 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(403)
			io.WriteString(w, `{"message":"You have exceeded a secondary rate limit"}`)
			return
		}
		io.WriteString(w, `{"data":{"r0":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}},
			"r1":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}},
			"r2":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}},
			"r3":{"issues":{"totalCount":0,"nodes":[]},"pullRequests":{"totalCount":0,"nodes":[]}}}}`)
	})
	repos := []model.Repo{infra, portal, {Owner: "acme", Name: "a"}, {Owner: "acme", Name: "b"}, {Owner: "acme", Name: "lonely"}}
	_, err := s.client(t).ListOpen(context.Background(), repos)
	if !IsRateLimit(err) {
		t.Fatalf("a rate limit on any chunk fails the call so the refresh backs off: %v", err)
	}
}

func TestSecondaryRateLimits(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	at, ok := retryAt(429, "", nil, now)
	if !ok || !at.Equal(now.Add(secondaryFloor)) {
		t.Errorf("a bare 429 waits the floor: %v %v", at, ok)
	}
	at, ok = retryAt(403, "You have exceeded a secondary rate limit. Please wait a few minutes before you try again.", nil, now)
	if !ok || !at.Equal(now.Add(secondaryFloor)) {
		t.Errorf("a secondary-limit 403 without headers waits the floor: %v %v", at, ok)
	}
	if _, ok := retryAt(403, "Resource not accessible by personal access token", nil, now); ok {
		t.Error("a permission 403 is not a rate limit")
	}
	if _, ok := retryAt(500, "", http.Header{"Retry-After": {"5"}}, now); ok {
		t.Error("only 403 and 429 are rate limits, whatever the headers say")
	}
	at, ok = retryAt(429, "", http.Header{"Retry-After": {"7"}}, now)
	if !ok || !at.Equal(now.Add(7*time.Second)) {
		t.Errorf("Retry-After wins over the floor: %v %v", at, ok)
	}
	date := now.Add(90 * time.Second)
	at, ok = retryAt(403, "abuse detection", http.Header{"Retry-After": {date.Format(http.TimeFormat)}}, now)
	if !ok || !at.Equal(date) {
		t.Errorf("an HTTP-date Retry-After is parsed: %v %v", at, ok)
	}
}

func TestDetailQueries(t *testing.T) {
	s := newServer(t)
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "pullRequest(number: $number)") && strings.Contains(req.Query, "reviews(last: 50)"):
			if req.Variables["number"] == float64(404) {
				io.WriteString(w, `{"data":{"repository":{"pullRequest":null}},"errors":[{"message":"Could not resolve to a PullRequest with the number of 404."}]}`)
				return
			}
			io.WriteString(w, `{"data":{"repository":{"pullRequest":{"number":3543,"title":"canary","body":"b","isDraft":false,"reviewDecision":"APPROVED",
				"baseRefName":"main","headRefName":"feat","headRefOid":"h1","state":"OPEN","additions":1,"deletions":2,"changedFiles":3,
				"author":{"login":"bob"},"commits":{"nodes":[{"commit":{"committedDate":"2026-10-21T09:00:00Z"}}]},
				"latestOpinionatedReviews":{"nodes":[]},
				"comments":{"totalCount":1,"nodes":[{"author":{"login":"carol"},"createdAt":"2026-10-21T10:00:00Z","body":"hi"}]},
				"reviews":{"totalCount":1,"nodes":[{"author":{"login":"alice"},"state":"APPROVED","submittedAt":"2026-10-21T11:00:00Z","body":""}]}}}}}`)
		case strings.Contains(req.Query, "pullRequest(number: $number)"):
			io.WriteString(w, `{"data":{"repository":{"pullRequest":{"comments":{"nodes":[{"author":{"login":"infra-bot"},"createdAt":"2026-10-21T10:00:00Z","body":"queued"}]}}}}}`)
		case strings.Contains(req.Query, "issue(number: $number)"):
			io.WriteString(w, `{"data":{"repository":{"issue":{"number":3530,"title":"an issue","body":"","state":"OPEN","createdAt":"2026-10-19T10:00:00Z",
				"author":{"login":"infra-bot"},"labels":{"nodes":[]},"comments":{"totalCount":0,"nodes":[]}}}}}`)
		case strings.Contains(req.Query, "assignableUsers"):
			io.WriteString(w, `{"data":{"repository":{"assignableUsers":{"nodes":[{"login":"alice","name":"Alice"},{"login":"bob","name":""}]}}}}`)
		case strings.Contains(req.Query, "labels: [$label]"):
			if req.Variables["label"] != "window-manifest" || req.Variables["first"] != float64(10) {
				t.Errorf("label variables: %v", req.Variables)
			}
			io.WriteString(w, `{"data":{"repository":{"issues":{"totalCount":1,"nodes":[{"number":3530,"title":"Window 2026-10-28 23:00 ET","body":"plan",
				"state":"OPEN","author":{"login":"infra-bot"},"labels":{"nodes":[{"name":"window-manifest"}]},
				"comments":{"totalCount":1,"nodes":[{"author":{"login":"infra-bot"},"createdAt":"2026-10-21T10:00:00Z","body":"c"}]}}]}}}}`)
		default:
			io.WriteString(w, `{"data":{"repository":null},"errors":[{"message":"nope"}]}`)
		}
	})
	cl := s.client(t)
	ctx := context.Background()
	pr, err := cl.PRDetail(ctx, infra, 3543)
	if err != nil || pr.Repo != infra || pr.Number != 3543 || pr.ChangedFiles != 3 || pr.Reviews.Nodes[0].Author.Login != "alice" || pr.Comments.Nodes[0].Body != "hi" {
		t.Fatalf("PRDetail: %+v %v", pr, err)
	}
	if _, err := cl.PRDetail(ctx, infra, 404); err == nil || err.Error() != "#404 is not a pull request in acme/infra" {
		t.Errorf("missing PR: %v", err)
	}
	is, err := cl.IssueDetail(ctx, infra, 3530)
	if err != nil || is.Repo != infra || is.Author.Login != "infra-bot" {
		t.Fatalf("IssueDetail: %+v %v", is, err)
	}
	users, err := cl.AssignableUsers(ctx, infra)
	if err != nil || len(users) != 2 || users[0].Name != "Alice" {
		t.Fatalf("AssignableUsers: %+v %v", users, err)
	}
	cms, err := cl.PRComments(ctx, infra, 3544)
	if err != nil || len(cms) != 1 || cms[0].Author.Login != "infra-bot" {
		t.Fatalf("PRComments: %+v %v", cms, err)
	}
	issues, err := cl.IssuesWithLabel(ctx, infra, "window-manifest", 0)
	if err != nil || len(issues) != 1 || issues[0].Repo != infra || len(issues[0].Comments.Nodes) != 1 {
		t.Fatalf("IssuesWithLabel: %+v %v", issues, err)
	}
}

func TestReleasePRs(t *testing.T) {
	s := newServer(t)
	var mu sync.Mutex
	calls := map[string]int{}
	var inflight, peak atomic.Int32
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		name, _ := req.Variables["name"].(string)
		mu.Lock()
		calls[name]++
		k := calls[name]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Variables["prod"] != "main" || req.Variables["prodRef"] != "refs/heads/main" || req.Variables["stageRef"] != "refs/heads/stage" {
			t.Errorf("branch variables: %v", req.Variables)
		}
		switch name {
		case "flaky":
			if k == 1 { // a network error gets one retry
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
		case "broken":
			w.WriteHeader(502)
			io.WriteString(w, `{"message":"Bad Gateway"}`)
			return
		case "ghost":
			io.WriteString(w, `{"data":{`+rateLimitJSON()+`,"r0":null},"errors":[{"path":["r0"],"message":"Could not resolve to a Repository with the name 'acme/ghost'."}]}`)
			return
		}
		num := 1000 + len(name)
		io.WriteString(w, `{"data":{`+rateLimitJSON()+`,"r0":{"nameWithOwner":"acme/`+name+`","isArchived":false,
			"prod":{"target":{"oid":"m-`+name+`"}},"stage":null,
			"open":{"totalCount":1,"nodes":[`+listPR(num, "Release "+name)+`]},
			"merged":{"nodes":[`+listPR(num-1, "Release old "+name)+`]}}}}`)
	})
	cl := s.client(t)
	var repos []model.Repo
	for _, n := range []string{"infra", "web-portal", "flaky", "broken", "ghost", "a", "b", "c", "d"} {
		repos = append(repos, model.Repo{Owner: "acme", Name: n})
	}
	out, err := cl.ReleasePRs(context.Background(), repos, "main", "stage")
	if err != nil {
		t.Fatalf("ReleasePRs: %v", err)
	}
	if peak.Load() > 6 {
		t.Errorf("more than six in flight: %d", peak.Load())
	}
	if calls["flaky"] != 2 || calls["infra"] != 1 {
		t.Errorf("one query per repo, a network error retried once: %v", calls)
	}
	r := out[infra]
	if r.Err != nil || r.Name != "acme/infra" || r.ProdOid != "m-infra" || r.StageOid != "" || len(r.PRs) != 2 ||
		r.PRs[0].Number != 1005 || r.PRs[1].Number != 1004 || r.PRs[0].Repo != infra {
		t.Errorf("infra: %+v", r)
	}
	if r = out[repos[2]]; r.Err != nil || len(r.PRs) != 2 {
		t.Errorf("flaky recovered: %+v", r)
	}
	if r = out[repos[3]]; r.Err == nil || !strings.Contains(r.Err.Error(), "502") || len(r.PRs) != 0 {
		t.Errorf("broken: one repo's error leaves the others intact: %+v", r)
	}
	if r = out[repos[4]]; r.Err == nil || !strings.Contains(r.Err.Error(), "repository not visible to this token: Could not resolve") {
		t.Errorf("ghost: %+v", r)
	}
	if out, err := cl.ReleasePRs(context.Background(), nil, "main", "stage"); err != nil || len(out) != 0 {
		t.Error("empty")
	}
}

func TestFortress(t *testing.T) {
	s := newServer(t)
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("Content-Type", "application/json")
		if req.Variables["o1"] != "acme" || req.Variables["n1"] != "web-portal" || !strings.Contains(req.Query, "f1: repository(owner: $o1, name: $n1)") {
			t.Errorf("fortress request: %v", req.Variables)
		}
		io.WriteString(w, `{"data":{`+rateLimitJSON()+`,"f0":{"branches":{"totalCount":120,"nodes":[{"name":"main","target":{"committedDate":"2026-10-28T09:00:00Z"}},null]},
			"defaultBranchRef":{"name":"main","target":{"history":{"nodes":[{"oid":"abc","messageHeadline":"fix","committedDate":"2026-10-28T09:00:00Z"}]}}},
			"open":{"nodes":[{"number":3543,"title":"canary","isDraft":false,"createdAt":"2026-10-20T10:00:00Z","baseRefName":"main","headRefName":"feat","reviewDecision":null,"author":{"login":"bob"}}]}},
			"f1":null}}`)
	})
	out, err := s.client(t).Fortress(context.Background(), []model.Repo{infra, portal})
	if err != nil {
		t.Fatal(err)
	}
	fd, ok := out[infra]
	if !ok || fd.BranchTotal != 120 || len(fd.Branches) != 1 || fd.Default != "main" || len(fd.Commits) != 1 || fd.Commits[0].Oid != "abc" ||
		len(fd.OpenPRs) != 1 || fd.OpenPRs[0].Repo != infra {
		t.Errorf("infra: %+v", fd)
	}
	if _, ok := out[portal]; ok {
		t.Error("an invisible repo is left out")
	}
}

func TestRESTOperations(t *testing.T) {
	s := newServer(t)
	s.jsonOK("POST", "/repos/acme/infra/pulls/3543/reviews", `{"id":1}`)
	s.jsonOK("POST", "/repos/acme/infra/issues/3543/comments", `{"id":2}`)
	s.jsonOK("PUT", "/repos/acme/infra/pulls/1042/merge", `{"sha":"m3rge","merged":true,"message":"Pull Request successfully merged"}`)
	s.handle("GET", "/repos/acme/infra/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("status") == "waiting" {
			io.WriteString(w, `{"workflow_runs":[{"id":7,"name":"apply","status":"waiting","head_sha":"h1","path":".github/workflows/apply.yml"}]}`)
			return
		}
		io.WriteString(w, `{"workflow_runs":[{"id":7,"name":"apply","status":"completed","conclusion":"success","head_sha":"h1","run_attempt":2,"event":"push"},{"id":8,"name":"plan","status":"in_progress"}]}`)
	})
	s.jsonOK("GET", "/repos/acme/infra/actions/runs/7/pending_deployments",
		`[{"environment":{"id":55,"name":"prod"},"wait_timer":0,"current_user_can_approve":true}]`)
	s.jsonOK("POST", "/repos/acme/infra/actions/runs/7/pending_deployments", `[]`)
	s.handle("GET", "/repos/acme/infra/pulls/1042/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("page")
		if page == "1" {
			var items []string
			for i := 0; i < 100; i++ {
				items = append(items, fmt.Sprintf(`{"filename":"f%d","status":"modified","additions":1,"deletions":0,"patch":"@@"}`, i))
			}
			io.WriteString(w, "["+strings.Join(items, ",")+"]")
			return
		}
		io.WriteString(w, `[{"filename":"last","status":"added"}]`)
	})
	s.handle("GET", "/repos/acme/infra/pulls/1042", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != acceptDiff {
			t.Errorf("diff Accept: %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "diff --git a/x b/x\n")
	})
	s.jsonOK("GET", "/repos/acme/infra/compare/main...stage",
		`{"ahead_by":3,"behind_by":0,"total_commits":3,"commits":[{"sha":"c1","commit":{"message":"one"}},{"sha":"c2","commit":{"message":"two\n\nbody"}}]}`)
	s.jsonOK("GET", "/repos/acme/infra/git/matching-refs/tags/go/",
		`[{"ref":"refs/tags/go/612"},{"ref":"refs/tags/go/613"},{"ref":"refs/heads/go/x"}]`)
	s.handle("GET", "/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			var items []string
			for i := 0; i < 100; i++ {
				items = append(items, fmt.Sprintf(`{"name":"r%d","archived":%v}`, i, i%2 == 0))
			}
			io.WriteString(w, "["+strings.Join(items, ",")+"]")
			return
		}
		io.WriteString(w, `[{"name":"infra","archived":false},{"archived":true}]`)
	})
	s.jsonOK("GET", "/repos/acme/infra/rulesets", `[{"name":"Release Standard","source_type":"Organization"},{"name":"main","source_type":"Repository"}]`)

	cl := s.client(t)
	ctx := context.Background()
	if err := cl.Approve(ctx, infra, 3543, "lgtm"); err != nil {
		t.Fatal(err)
	}
	_, body := s.last()
	if body != `{"body":"lgtm","event":"APPROVE"}` {
		t.Errorf("approve body: %s", body)
	}
	if err := cl.Comment(ctx, infra, 3543, "/approve"); err != nil {
		t.Fatal(err)
	}
	if _, body = s.last(); body != `{"body":"/approve"}` {
		t.Errorf("comment body: %s", body)
	}
	res, err := cl.MergePR(ctx, infra, 1042, "h1", "Release 2026-10-27", "")
	if err != nil || !res.Merged || res.SHA != "m3rge" {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if _, body = s.last(); body != `{"commit_title":"Release 2026-10-27","merge_method":"merge","sha":"h1"}` {
		t.Errorf("merge body: %s", body)
	}
	if _, err := cl.MergePR(ctx, infra, 1042, "", "", "squash"); err != nil {
		t.Fatal(err)
	}
	if _, body = s.last(); body != `{"merge_method":"squash"}` {
		t.Errorf("squash body: %s", body)
	}
	runs, err := cl.WaitingRuns(ctx, infra, "h1")
	if err != nil || len(runs) != 1 || runs[0].ID != 7 || runs[0].Path != ".github/workflows/apply.yml" {
		t.Fatalf("waiting runs: %+v %v", runs, err)
	}
	req, _ := s.last()
	if req.URL.RawQuery != "head_sha=h1&status=waiting&per_page=50" {
		t.Errorf("waiting query: %s", req.URL.RawQuery)
	}
	runs, err = cl.WorkflowRuns(ctx, infra, "h1")
	if err != nil || len(runs) != 2 || runs[0].RunAttempt != 2 || runs[1].Status != "in_progress" {
		t.Fatalf("workflow runs: %+v %v", runs, err)
	}
	pend, err := cl.PendingDeployments(ctx, infra, 7)
	if err != nil || len(pend) != 1 || pend[0].Environment.ID != 55 || !pend[0].CurrentUserCanApprove {
		t.Fatalf("pending: %+v %v", pend, err)
	}
	if err := cl.ApproveDeployments(ctx, infra, 7, []int64{55}, "go"); err != nil {
		t.Fatal(err)
	}
	if _, body = s.last(); body != `{"comment":"go","environment_ids":[55],"state":"approved"}` {
		t.Errorf("approve deployments body: %s", body)
	}
	if err := cl.ApproveDeployments(ctx, infra, 7, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, body = s.last(); body != `{"comment":"","environment_ids":[],"state":"approved"}` {
		t.Errorf("nil env ids: %s", body)
	}
	files, err := cl.PRFiles(ctx, infra, 1042)
	if err != nil || len(files) != 101 || files[100].Filename != "last" || files[0].Patch != "@@" {
		t.Fatalf("files: %d %v", len(files), err)
	}
	diff, err := cl.PRDiff(ctx, infra, 1042)
	if err != nil || !strings.HasPrefix(diff, "diff --git") {
		t.Fatalf("diff: %q %v", diff, err)
	}
	cmp, err := cl.Compare(ctx, infra, "main", "stage")
	if err != nil || cmp.AheadBy != 3 || len(cmp.Commits) != 2 || cmp.Commits[1].Message != "two\n\nbody" || cmp.Commits[0].SHA != "c1" {
		t.Fatalf("compare: %+v %v", cmp, err)
	}
	if req, _ = s.last(); req.URL.RawQuery != "per_page=250" {
		t.Errorf("compare query: %s", req.URL.RawQuery)
	}
	tags, err := cl.TagRefs(ctx, infra, "go/")
	if err != nil || strings.Join(tags, ",") != "go/612,go/613" {
		t.Fatalf("tags: %v %v", tags, err)
	}
	repos, err := cl.OrgRepos(ctx, "acme")
	if err != nil || len(repos) != 101 || repos[100].Name != "infra" || !repos[0].Archived {
		t.Fatalf("org repos: %d %v", len(repos), err)
	}
	rs, err := cl.Rulesets(ctx, infra)
	if err != nil || len(rs) != 2 || rs[0].Name != "Release Standard" || rs[0].SourceType != "Organization" {
		t.Fatalf("rulesets: %+v %v", rs, err)
	}
	// an empty 2xx body on a write is fine
	s.handle("POST", "/repos/acme/infra/issues/1/comments", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	if err := cl.Comment(ctx, infra, 1, "x"); err != nil {
		t.Errorf("204: %v", err)
	}
}

func TestNewHonoursGHHost(t *testing.T) {
	t.Setenv("GH_HOST", "ghe.acme.test")
	c, err := New(Options{Token: "x"})
	if err != nil || c.Host() != "ghe.acme.test" {
		t.Errorf("GH_HOST: %v %q", err, c.Host())
	}
	c, err = New(Options{Host: "other.test", Token: "x"})
	if err != nil || c.Host() != "other.test" {
		t.Errorf("an explicit host wins: %v %q", err, c.Host())
	}
	t.Setenv("GH_HOST", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	c, err = New(Options{Token: "x"})
	if err != nil || c.Host() != "github.com" {
		t.Errorf("default: %v %q", err, c.Host())
	}
}

func TestNoTokenError(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
	_, err := New(Options{Host: "ghe.nowhere.test"})
	var nt *NoTokenError
	if !errors.As(err, &nt) || nt.Host != "ghe.nowhere.test" || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("no token: %v", err)
	}
}
