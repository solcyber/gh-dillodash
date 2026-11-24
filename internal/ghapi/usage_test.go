package ghapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

func TestPathClassHidesWhatWasRead(t *testing.T) {
	cases := map[string]string{
		"https://api.github.com/repos/acme/web/actions/runs?status=waiting&per_page=50":                    "repos/R/actions/runs?status=waiting&per_page=50",
		"https://api.github.com/repos/acme/web/actions/runs/4532/pending_deployments":                      "repos/R/actions/runs/N/pending_deployments",
		"https://ghe.example/api/v3/repos/a/b/commits/0123456789abcdef0123456789abcdef01234567/check-runs": "repos/R/commits/N/check-runs",
		"https://api.github.com/repos/a/b/actions/runs?head_sha=0123456789abcdef&status=waiting":           "repos/R/actions/runs?head_sha=SHA&status=waiting",
		"https://api.github.com/orgs/acme/repos?per_page=100":                                              "orgs/O/repos?per_page=100",
	}
	for in, want := range cases {
		if got := PathClass(in); got != want {
			t.Errorf("PathClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueryClassNamesTheFirstField(t *testing.T) {
	cases := map[string]string{
		listQuery(2):                         "r0",
		prDetailQuery:                        "repository",
		promoteSearchQuery():                 "s", // its alias
		withCost(prHeadQuery):                "repository",
		"mutation { addX(input: {}) { x } }": "addX",
	}
	for in, want := range cases {
		if got := QueryClass(in); got != want {
			t.Errorf("QueryClass(%.40q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithCostAsksOnlyQueriesWithoutARateLimit(t *testing.T) {
	q := withCost(prHeadQuery)
	if !strings.Contains(q, "rateLimit { cost }") {
		t.Fatalf("a query without rateLimit gains it:\n%s", q)
	}
	if got := withCost(listQuery(1)); strings.Count(got, "rateLimit") != 1 {
		t.Errorf("a query that reads its rate limit is left alone:\n%s", got)
	}
	m := "mutation($i: X!) { addX(input: $i) { x } }"
	if withCost(m) != m {
		t.Errorf("a mutation is left alone")
	}
}

func TestSourcesNest(t *testing.T) {
	ctx := context.Background()
	if SourceOf(ctx) != SourceAction {
		t.Fatalf("untagged = %q", SourceOf(ctx))
	}
	ctx = WithSource(ctx, "refresh")
	ctx = WithSource(ctx, "release_pr")
	if got := SourceOf(ctx); got != "refresh:release_pr" {
		t.Errorf("nested = %q", got)
	}
}

func TestUsageCountsBySourceAndReadsTheBudget(t *testing.T) {
	s := newServer(t)
	reset := time.Now().Add(40 * time.Minute).Unix()
	etag := `"v1"`
	s.handle("GET", "/repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4100")
		w.Header().Set("X-RateLimit-Used", "900")
		w.Header().Set("X-RateLimit-Reset", itoa(reset))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"workflow_runs":[]}`)
	})
	s.setGraphQL(func(w http.ResponseWriter, req gqlReq) {
		w.Header().Set("X-RateLimit-Resource", "graphql")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "1200")
		w.Header().Set("X-RateLimit-Reset", itoa(reset))
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(req.Query, "rateLimit") {
			t.Errorf("the query was not asked its cost:\n%s", req.Query)
		}
		io.WriteString(w, `{"data":{"rateLimit":{"cost":7},"repository":{"pullRequest":{"headRefOid":"abc","state":"OPEN"}}}}`)
	})
	u := NewUsage()
	var trace bytes.Buffer
	u.SetTrace(&trace)
	c := s.client(t)
	c.usage = u
	poll := WithSource(context.Background(), "poll:waiting_runs")
	for range 3 {
		if _, err := c.RepoWaitingRuns(poll, model.Repo{Owner: "o", Name: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := c.PRHead(WithSource(context.Background(), "detail"), model.Repo{Owner: "o", Name: "r"}, 7); err != nil {
		t.Fatal(err)
	}
	snap := u.Snapshot()
	if got := snap.By["poll:waiting_runs"]; got.REST != 1 || got.REST304 != 2 {
		t.Errorf("poll counts = %+v, want 1 answered and 2 not modified", got)
	}
	if got := snap.By["detail"]; got.GQL != 1 || got.GQLCost != 7 {
		t.Errorf("detail counts = %+v, want one query costing 7", got)
	}
	b, ok := snap.Tightest(time.Now())
	if !ok || b.Resource != "graphql" || b.Remaining != 1200 {
		t.Errorf("tightest = %+v, %v; want graphql 1200", b, ok)
	}
	if snap.Budgets["core"].Used != 900 {
		t.Errorf("core = %+v", snap.Budgets["core"])
	}
	lines := strings.Split(strings.TrimSpace(trace.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("trace lines = %d:\n%s", len(lines), trace.String())
	}
	var first, last traceLine
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[3]), &last)
	if first.Path != "repos/R/actions/runs?status=waiting&per_page=50" || first.Source != "poll:waiting_runs" {
		t.Errorf("first trace line = %+v", first)
	}
	if last.Cost != 7 || !strings.HasPrefix(last.Path, "graphql ") {
		t.Errorf("graphql trace line = %+v", last)
	}
	if strings.Contains(trace.String(), "/o/r/") {
		t.Errorf("the trace names the repo:\n%s", trace.String())
	}
}

func TestTightestIgnoresAResetWindow(t *testing.T) {
	now := time.Now()
	s := UsageSnapshot{Budgets: map[string]Budget{
		"graphql": {Resource: "graphql", Limit: 5000, Remaining: 10, Reset: now.Add(-time.Minute), At: now.Add(-2 * time.Minute)},
		"core":    {Resource: "core", Limit: 5000, Remaining: 4000, Reset: now.Add(time.Hour), At: now},
	}}
	b, ok := s.Tightest(now)
	if !ok || b.Resource != "core" {
		t.Errorf("tightest = %+v; a window that has reset is full again", b)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
