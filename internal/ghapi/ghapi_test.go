package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

var (
	infra  = model.Repo{Owner: "acme", Name: "infra"}
	portal = model.Repo{Owner: "acme", Name: "web-portal"}
)

// ---- test_rate_limit.py: header parsing and error tagging ----------------

func TestRetryAtFromHeaders(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	// Secondary limit: Retry-After is a delta in seconds.
	at, ok := RetryAtFromHeaders(http.Header{"Retry-After": {"42"}}, now)
	if !ok || !at.Equal(now.Add(42*time.Second)) {
		t.Errorf("Retry-After -> now+delta: %v %v", at, ok)
	}
	// Primary limit: Remaining 0 + Reset (absolute epoch).
	reset := now.Unix() + 1800
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
	at, ok = RetryAtFromHeaders(h, now)
	if !ok || at.Unix() != reset {
		t.Errorf("Remaining=0 -> Reset epoch: %v %v", at, ok)
	}
	// Budget left, or no headers at all: not a rate limit.
	if _, ok := RetryAtFromHeaders(http.Header{"X-Ratelimit-Remaining": {"57"}}, now); ok {
		t.Error("Remaining>0 -> not a rate limit")
	}
	if _, ok := RetryAtFromHeaders(nil, now); ok {
		t.Error("no headers -> not a rate limit")
	}
	if _, ok := RetryAtFromHeaders(http.Header{"Retry-After": {"Wed, 21 Oct 2015 07:28:00 GMT"}}, now); ok {
		t.Error("a non-numeric Retry-After is ignored")
	}
	if _, ok := RetryAtFromHeaders(http.Header{"X-Ratelimit-Remaining": {"0"}}, now); ok {
		t.Error("Remaining=0 without Reset is not a rate limit")
	}
}

func TestErrorShape(t *testing.T) {
	// A plain error carries no RetryAt.
	e := &Error{Method: "POST", URL: "https://api.github.com/graphql", Status: 502, Message: "Bad Gateway"}
	if e.IsRateLimit() {
		t.Error("default RetryAt is zero")
	}
	if e.Error() != "POST https://api.github.com/graphql -> 502: Bad Gateway" {
		t.Errorf("Error(): %q", e.Error())
	}
	n := &Error{Message: "network error: timed out"}
	if n.Error() != "network error: timed out" || !IsNetwork(n) {
		t.Errorf("network error shape: %q", n.Error())
	}
	wrapped := errors.Join(errors.New("outer"), &Error{RetryAt: time.Now()})
	if !IsRateLimit(wrapped) {
		t.Error("IsRateLimit sees through wrapping")
	}
	if IsRateLimit(errors.New("plain")) || IsNetwork(nil) {
		t.Error("non-Errors are neither")
	}
}

// ---- the app's backoff (test_approve.py: refresh failure cadence) -------

func TestBackoff(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	b := &Backoff{Refresh: 300 * time.Second}
	if b.Wait(now) != 0 || !b.Until().IsZero() {
		t.Error("fresh backoff is unconstrained")
	}
	plain := &Error{Message: "refresh failed: kaboom"}
	// first failure retries in ~15s
	if got := b.Fail(now, plain); !got.Equal(now.Add(15 * time.Second)) {
		t.Errorf("first failure: %v", got.Sub(now))
	}
	if b.Wait(now) != 15*time.Second || b.Streak() != 1 {
		t.Errorf("wait %v streak %d", b.Wait(now), b.Streak())
	}
	// second failure backs off to ~30s
	if got := b.Fail(now, plain); !got.Equal(now.Add(30 * time.Second)) {
		t.Errorf("second failure: %v", got.Sub(now))
	}
	for i := 0; i < 10; i++ {
		b.Fail(now, plain)
	}
	// 15s * 2^8 = 3840s, capped at the refresh interval
	if got := b.Wait(now); got != 300*time.Second {
		t.Errorf("capped at refresh: %v", got)
	}
	if b.RateLimitWait(now) != 0 {
		t.Error("an exponential backoff is not a rate wall")
	}
	// success: streak reset, normal cadence
	b.OK()
	if b.Streak() != 0 || b.Wait(now) != 0 {
		t.Error("OK resets")
	}
	// a rate limit holds until GitHub's reset, whatever the streak
	rl := &Error{Status: 403, Message: "API rate limit exceeded", RetryAt: now.Add(100 * time.Second)}
	if got := b.Fail(now, rl); !got.Equal(rl.RetryAt) {
		t.Errorf("rate limit: %v", got)
	}
	if b.RateLimitWait(now) != 100*time.Second || b.Wait(now.Add(50*time.Second)) != 50*time.Second {
		t.Errorf("rate wall: %v", b.RateLimitWait(now))
	}
	if b.Wait(now.Add(101*time.Second)) != 0 || b.RateLimitWait(now.Add(101*time.Second)) != 0 {
		t.Error("the wall lifts by itself")
	}
	// an uncapped backoff (Refresh 0) still doubles up to 2^8
	u := &Backoff{}
	for i := 0; i < 20; i++ {
		u.Fail(now, plain)
	}
	if u.Wait(now) != 3840*time.Second {
		t.Errorf("uncapped: %v", u.Wait(now))
	}
	// a wrapped non-Error counts as a plain failure
	w := &Backoff{}
	w.Fail(now, errors.New("x"))
	if w.Streak() != 1 {
		t.Error("plain error counted")
	}
}

// ---- Actions-fallback CI cache (test_approve.py) ------------------------

type runsScript struct {
	calls int
	runs  []model.WorkflowRun
	err   error
}

func (s *runsScript) fetch(ctx context.Context, repo model.Repo, sha string) ([]model.WorkflowRun, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.runs, nil
}

func run(path, name, status, conclusion string) model.WorkflowRun {
	return model.WorkflowRun{Path: path, Name: name, Status: status, Conclusion: conclusion}
}

func TestCIFallbackCache(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := &runsScript{runs: []model.WorkflowRun{run(".github/workflows/plan.yml", "plan", "completed", "failure")}}
	f := &CIFallback{}
	pr := &model.PR{Repo: infra, Number: 9, HeadRefOid: "s9"}
	ctx := context.Background()

	cl, warn, err := f.Annotate(ctx, now, []*model.PR{pr}, s.fetch)
	if err != nil || warn != "" || cl[0].Glyphs != "x" || cl[0].Failed[0] != "plan" {
		t.Fatalf("first: %+v %q %v", cl, warn, err)
	}
	// a failing settled SHA is re-polled (re-run seen)
	s.runs = []model.WorkflowRun{run(".github/workflows/plan.yml", "plan", "completed", "success")}
	cl, _, _ = f.Annotate(ctx, now, []*model.PR{pr}, s.fetch)
	if s.calls != 2 || cl[0].Glyphs != "+" || len(cl[0].Failed) != 0 {
		t.Errorf("re-run seen: calls %d %+v", s.calls, cl[0])
	}
	// a passing settled SHA is not re-polled
	f.Annotate(ctx, now.Add(time.Minute), []*model.PR{pr}, s.fetch)
	if s.calls != 2 {
		t.Errorf("passing settled re-polled: %d", s.calls)
	}
	// ...until the TTL
	f.Age("s9", CIRecheck+time.Second)
	f.Annotate(ctx, now.Add(time.Minute), []*model.PR{pr}, s.fetch)
	if s.calls != 3 {
		t.Errorf("TTL re-poll: %d", s.calls)
	}
	// Flush forgets settled clusters too (the manual refresh)
	f.Flush()
	f.Annotate(ctx, now, []*model.PR{pr}, s.fetch)
	if s.calls != 4 {
		t.Errorf("flush: %d", s.calls)
	}
}

func TestCIFallbackInFlightAndDedupe(t *testing.T) {
	now := time.Now()
	s := &runsScript{runs: []model.WorkflowRun{
		run(".github/workflows/plan.yml", "plan", "in_progress", ""),
		run(".github/workflows/plan.yml", "plan", "completed", "failure"), // an older attempt
		run(".github/workflows/lint.yml", "lint", "completed", "success"),
		run(".github/workflows/deploy.yml", "deploy", "queued", ""),
		run("", "unnamed", "completed", "skipped"),
	}}
	f := &CIFallback{}
	pr := &model.PR{Repo: infra, Number: 9, HeadRefOid: "s9"}
	cl, _, _ := f.Annotate(context.Background(), now, []*model.PR{pr}, s.fetch)
	if cl[0].Glyphs != "~+.-" || cl[0].Tone != model.CIToneDim {
		t.Errorf("newest run per workflow wins: %+v", cl[0])
	}
	// in flight: re-polled every refresh
	f.Annotate(context.Background(), now, []*model.PR{pr}, s.fetch)
	if s.calls != 2 {
		t.Errorf("in-flight re-poll: %d", s.calls)
	}
	// an empty result (fresh push) is never settled
	s.runs = nil
	cl, _, _ = f.Annotate(context.Background(), now, []*model.PR{pr}, s.fetch)
	f.Annotate(context.Background(), now, []*model.PR{pr}, s.fetch)
	if s.calls != 4 || cl[0].Glyphs != "" {
		t.Errorf("empty keeps polling: %d %+v", s.calls, cl[0])
	}
}

func TestCIFallbackRollupWinsAndUnknown(t *testing.T) {
	s := &runsScript{}
	f := &CIFallback{}
	visible := prWithRollup(infra, 1, "a", `{"__typename":"CheckRun","name":"plan","status":"COMPLETED","conclusion":"SUCCESS"}`)
	noSHA := &model.PR{Repo: infra, Number: 2}
	cl, warn, err := f.Annotate(context.Background(), time.Now(), []*model.PR{visible, noSHA}, s.fetch)
	if err != nil || s.calls != 0 {
		t.Fatalf("rollup needs no fetch: %d %v", s.calls, err)
	}
	if cl[0].Glyphs != "+" || cl[1].Glyphs != "?" || warn != CINotVisible {
		t.Errorf("%+v %q", cl, warn)
	}
	// no fetch function at all: unknown, with the warning
	cl, warn, _ = f.Annotate(context.Background(), time.Now(), []*model.PR{{Repo: infra, HeadRefOid: "z"}}, nil)
	if cl[0].Glyphs != "?" || warn != CINotVisible {
		t.Errorf("nil fetch: %+v %q", cl, warn)
	}
}

func TestCIFallbackLatchAndRateLimit(t *testing.T) {
	now := time.Now()
	f := &CIFallback{}
	forbidden := &runsScript{err: &Error{Method: "GET", URL: "u", Status: 403, Message: "Resource not accessible by personal access token"}}
	infraPR := &model.PR{Repo: infra, Number: 1, HeadRefOid: "a"}
	portalPR := &model.PR{Repo: portal, Number: 2, HeadRefOid: "b"}
	cl, warn, err := f.Annotate(context.Background(), now, []*model.PR{infraPR}, forbidden.fetch)
	if err != nil || cl[0].Glyphs != "?" || !strings.Contains(warn, "token needs Actions: read") || !strings.Contains(warn, "403") {
		t.Fatalf("403 latches: %+v %q %v", cl, warn, err)
	}
	// latched per repo: infra is never asked again, web-portal still is
	ok := &runsScript{runs: []model.WorkflowRun{run("p", "plan", "completed", "success")}}
	cl, warn, _ = f.Annotate(context.Background(), now, []*model.PR{infraPR, portalPR}, ok.fetch)
	if ok.calls != 1 || cl[0].Glyphs != "?" || cl[1].Glyphs != "+" || !strings.Contains(warn, "Actions: read") {
		t.Errorf("per-repo latch: calls %d %+v %q", ok.calls, cl, warn)
	}
	if f.Disabled(infra) == "" || f.Disabled(portal) != "" {
		t.Error("Disabled reports the latch")
	}
	f.Reset()
	if f.Disabled(infra) != "" {
		t.Error("Reset lifts the latch")
	}
	// a rate limit must surface, not latch
	rl := &runsScript{err: &Error{Status: 403, Message: "API rate limit exceeded", RetryAt: now.Add(time.Minute)}}
	_, _, err = f.Annotate(context.Background(), now, []*model.PR{infraPR}, rl.fetch)
	if !IsRateLimit(err) || f.Disabled(infra) != "" {
		t.Errorf("rate limit: %v latched=%q", err, f.Disabled(infra))
	}
}

func TestCIFallbackPrune(t *testing.T) {
	f := &CIFallback{}
	s := &runsScript{runs: []model.WorkflowRun{run("p", "plan", "completed", "success")}}
	// one live PR per refresh: the cache is trimmed to the live set once it
	// passes four entries
	for i := 0; i < 10; i++ {
		pr := &model.PR{Repo: infra, Number: i, HeadRefOid: "sha" + strconv.Itoa(i)}
		f.Annotate(context.Background(), time.Now(), []*model.PR{pr}, s.fetch)
	}
	if _, _, ok := f.Cached("sha0"); ok {
		t.Error("sha0 should have been pruned")
	}
	if _, _, ok := f.Cached("sha9"); !ok {
		t.Error("the live sha stays")
	}
	// ten live PRs at once all stay
	var prs []*model.PR
	for i := 0; i < 10; i++ {
		prs = append(prs, &model.PR{Repo: infra, Number: i, HeadRefOid: "live" + strconv.Itoa(i)})
	}
	f.Annotate(context.Background(), time.Now(), prs, s.fetch)
	for i := 0; i < 10; i++ {
		if _, _, ok := f.Cached("live" + strconv.Itoa(i)); !ok {
			t.Errorf("live%d pruned", i)
		}
	}
}

// prWithRollup is a PR whose head commit carries the given check runs,
// built through JSON like the real thing.
func prWithRollup(repo model.Repo, number int, sha string, checks string) *model.PR {
	pr := &model.PR{}
	raw := `{"number":` + strconv.Itoa(number) + `,"headRefOid":"` + sha + `","commits":{"nodes":[{"commit":{` +
		`"committedDate":"2026-10-28T10:00:00Z","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":1,"nodes":[` + checks + `]}}}}]}}`
	if err := json.Unmarshal([]byte(raw), pr); err != nil {
		panic(err)
	}
	pr.Repo = repo
	return pr
}

// ---- URLs and query shapes ----------------------------------------------

func TestWebURLs(t *testing.T) {
	if got := PRURL("github.com", infra, 3543); got != "https://github.com/acme/infra/pull/3543" {
		t.Errorf("pr: %s", got)
	}
	if got := IssueURL("", infra, 3530); got != "https://github.com/acme/infra/issues/3530" {
		t.Errorf("issue: %s", got)
	}
	if got := CompareURL("ghe.acme.test", portal, "main", "stage"); got != "https://ghe.acme.test/acme/web-portal/compare/main...stage" {
		t.Errorf("compare: %s", got)
	}
	if got := TreeURL("GitHub.com", infra, "release/2026-10-27"); got != "https://github.com/acme/infra/tree/release/2026-10-27" {
		t.Errorf("tree: %s", got)
	}
	if got := CommitURL("github.com", infra, "abc123"); got != "https://github.com/acme/infra/commit/abc123" {
		t.Errorf("commit: %s", got)
	}
	if got := WebURL("github.com", infra, "", ""); got != "https://github.com/acme/infra" {
		t.Errorf("repo: %s", got)
	}
	if RESTBase("github.com") != "https://api.github.com/" || RESTBase("ghe.acme.test") != "https://ghe.acme.test/api/v3/" {
		t.Errorf("rest bases: %s %s", RESTBase("github.com"), RESTBase("ghe.acme.test"))
	}
	if GraphQLURL("github.com") != "https://api.github.com/graphql" || GraphQLURL("ghe.acme.test") != "https://ghe.acme.test/api/graphql" {
		t.Errorf("graphql urls: %s %s", GraphQLURL("github.com"), GraphQLURL("ghe.acme.test"))
	}
}

func TestQueryShapes(t *testing.T) {
	q := listQuery(2)
	for _, want := range []string{"$o0: String!", "$n1: String!", "r0: repository(owner: $o0, name: $n0) { ...ListRepo }",
		"r1: repository(owner: $o1, name: $n1)", "fragment ListRepo on Repository", "rateLimit { limit remaining resetAt }",
		"pullRequests(states: OPEN, last: 100", "statusCheckRollup"} {
		if !strings.Contains(q, want) {
			t.Errorf("list query lacks %q", want)
		}
	}
	if strings.Contains(q, "$o2") {
		t.Error("list query has an extra alias")
	}
	fq := fortressQuery(3)
	if !strings.Contains(fq, "f2: repository(owner: $o2, name: $n2)") || !strings.Contains(fq, "defaultBranchRef") {
		t.Errorf("fortress query: %s", fq)
	}
	// release_query asks for open and recently merged PRs separately,
	// with the branch names as variables
	for _, want := range []string{"open: pullRequests(states: [OPEN], baseRefName: $prod",
		"merged: pullRequests(states: [MERGED], baseRefName: $prod", "ref(qualifiedName: $prodRef)",
		"ref(qualifiedName: $stageRef)", "fragment RelPR on PullRequest"} {
		if !strings.Contains(releaseQuery, want) {
			t.Errorf("release query lacks %q", want)
		}
	}
	if strings.Contains(releaseQuery, `"main"`) || strings.Contains(releaseQuery, `"stage"`) {
		t.Error("release query inlines a branch name")
	}
	if !strings.Contains(issuesWithLabelQuery, "labels: [$label]") || strings.Contains(issuesWithLabelQuery, "window-manifest") {
		t.Error("the label is a variable")
	}
}
