// Package ghapi is the dashboard's GitHub API layer: REST and GraphQL
// through go-gh's authenticated HTTP client, every failure as an *Error
// that knows whether GitHub asked us to back off, and a Client interface
// the UI can take a fake of.
package ghapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// DefaultTimeout bounds every request.
const DefaultTimeout = 10 * time.Second

// ListResult is one repo's share of ListOpen.
type ListResult struct {
	PRs    []*model.PR
	Issues []*model.Issue
	// Notes are warnings for the warn line: issues hidden by the token's
	// permissions, or more open than one page shows.
	Notes []string
	// Err is set when the repo itself was not visible (a typo, or a
	// fine-grained PAT still pending org approval).
	Err error
}

// MergeResult is PUT /pulls/N/merge's answer.
type MergeResult struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// CompareCommit is one commit of a compare.
type CompareCommit struct {
	SHA     string
	Message string
	// Parents are the parent shas (first parent first).
	Parents []string
}

// CompareResult is REST compare base...head (the first page, up to 250
// commits).
type CompareResult struct {
	AheadBy      int
	BehindBy     int
	TotalCommits int
	Commits      []CompareCommit
}

// FortressBranch is a branch with its tip's commit date.
type FortressBranch struct {
	Name          string
	CommittedDate time.Time
}

// FortressCommit is one default-branch commit.
type FortressCommit struct {
	Oid             string
	MessageHeadline string
	CommittedDate   time.Time
}

// FortressData is a repo's fortress-map slice: newest branches, the
// default branch's recent history, and the open PRs.
type FortressData struct {
	Branches    []FortressBranch
	BranchTotal int
	Default     string
	Commits     []FortressCommit
	OpenPRs     []*model.PR
}

// ReleaseRepo is one repo's release state: its open and recently merged
// PRs into the production branch, the branch tips, and Err for a repo
// that could not be read (anything but a rate limit, which fails the
// whole call).
type ReleaseRepo struct {
	Name     string
	PRs      []*model.PR
	ProdOid  string
	StageOid string
	Archived bool
	Err      error
}

// Client is every GitHub operation the dashboard performs.
type Client interface {
	// Whoami is the token's login.
	Whoami(ctx context.Context) (string, error)
	// ListOpen reads every repo's open PRs and issues, one aliased
	// GraphQL request per chunk of four repos run concurrently. A repo
	// the token cannot see gets a ListResult.Err, never a global failure.
	ListOpen(ctx context.Context, repos []model.Repo) (map[model.Repo]ListResult, error)
	// LastRate is the GraphQL budget from the latest list-shaped query.
	LastRate() *model.RateLimit

	PRDetail(ctx context.Context, repo model.Repo, number int) (*model.PR, error)
	IssueDetail(ctx context.Context, repo model.Repo, number int) (*model.Issue, error)
	AssignableUsers(ctx context.Context, repo model.Repo) ([]model.Actor, error)
	PRComments(ctx context.Context, repo model.Repo, number int) ([]model.Comment, error)
	// IssuesWithLabel is the open issues carrying label (newest first,
	// at most first), each with its last 100 comments.
	IssuesWithLabel(ctx context.Context, repo model.Repo, label string, first int) ([]*model.Issue, error)

	Approve(ctx context.Context, repo model.Repo, number int, body string) error
	Comment(ctx context.Context, repo model.Repo, number int, body string) error
	// MergePR merges only if the head is still sha (GitHub refuses
	// otherwise). method is merge, squash or rebase ("" = merge).
	MergePR(ctx context.Context, repo model.Repo, number int, sha, title, method string) (MergeResult, error)

	// WaitingRuns is the workflow runs on headSHA waiting on an environment.
	WaitingRuns(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error)
	PendingDeployments(ctx context.Context, repo model.Repo, runID int64) ([]model.PendingDeployment, error)
	ApproveDeployments(ctx context.Context, repo model.Repo, runID int64, envIDs []int64, comment string) error
	// WorkflowRuns is every run on headSHA, newest first.
	WorkflowRuns(ctx context.Context, repo model.Repo, headSHA string) ([]model.WorkflowRun, error)

	// PRFiles is the PR's changed files (up to ten pages of 100).
	PRFiles(ctx context.Context, repo model.Repo, number int) ([]model.PRFile, error)
	PRDiff(ctx context.Context, repo model.Repo, number int) (string, error)
	Compare(ctx context.Context, repo model.Repo, base, head string) (CompareResult, error)
	// TagRefs is the tag names under prefix ("go/" -> ["go/612", ...]).
	TagRefs(ctx context.Context, repo model.Repo, prefix string) ([]string, error)
	OrgRepos(ctx context.Context, org string) ([]model.RepoListing, error)
	Rulesets(ctx context.Context, repo model.Repo) ([]model.Ruleset, error)
	// FileContents reads one file from a repo (its default branch, or ref):
	// the bytes and the blob sha.
	FileContents(ctx context.Context, repo model.Repo, path, ref string) ([]byte, string, error)

	Fortress(ctx context.Context, repos []model.Repo) (map[model.Repo]FortressData, error)
	// ReleasePRs reads every repo's release PRs into prodBranch and the
	// tips of prodBranch and stageBranch, one query per repo with at
	// most six in flight. A rate limit fails the whole call.
	ReleasePRs(ctx context.Context, repos []model.Repo, prodBranch, stageBranch string) (map[model.Repo]ReleaseRepo, error)
}

// Options configure New.
type Options struct {
	// Host is github.com or a GHES host; "" means go-gh's default host.
	Host string
	// Token overrides the token go-gh would find for Host.
	Token string
	// Version goes into the User-Agent ("dillodash/<version>").
	Version string
	// Timeout bounds each request (DefaultTimeout when zero).
	Timeout time.Duration
	// BaseURL is an escape hatch for tests: when set, every REST path is
	// resolved against it and GraphQL posts to BaseURL + "/graphql",
	// instead of the URLs derived from Host. The go-gh HTTP client (and
	// so its headers and token) is still used, except that go-gh only
	// attaches Authorization when the request host matches Host — set
	// Host to the test server's hostname to see the token.
	BaseURL string
	// Transport replaces the default round tripper (tests).
	Transport http.RoundTripper
}

// GitHub is the real Client.
type GitHub struct {
	host        string
	tokenSource string
	http        *http.Client
	restBase    string // with trailing slash
	graphqlURL  string
	lastRate    atomic.Pointer[model.RateLimit]
}

var _ Client = (*GitHub)(nil)

// New builds a client. The token comes from Options.Token or
// auth.TokenForHost (env, gh config, or the keyring via gh); TokenSource
// says which.
func New(opts Options) (*GitHub, error) {
	host := NormalizeHost(opts.Host)
	if strings.TrimSpace(opts.Host) == "" {
		if h, _ := auth.DefaultHost(); h != "" {
			host = NormalizeHost(h)
		}
	}
	token, source := opts.Token, "option"
	if token == "" {
		token, source = auth.TokenForHost(host)
		if token == "" {
			return nil, fmt.Errorf("no GitHub token for %s: run `gh auth login` or set GH_TOKEN", host)
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	hc, err := api.NewHTTPClient(api.ClientOptions{
		Host:         host,
		AuthToken:    token,
		Headers:      map[string]string{"User-Agent": "dillodash/" + version},
		Timeout:      timeout,
		Transport:    opts.Transport,
		LogIgnoreEnv: true, // GH_DEBUG would write to the terminal under the TUI
	})
	if err != nil {
		return nil, err
	}
	c := &GitHub{
		host:        host,
		tokenSource: source,
		http:        hc,
		restBase:    RESTBase(host),
		graphqlURL:  GraphQLURL(host),
	}
	if opts.BaseURL != "" {
		base := strings.TrimRight(opts.BaseURL, "/")
		c.restBase = base + "/"
		c.graphqlURL = base + "/graphql"
	}
	return c, nil
}

// Host is the GitHub host the client talks to.
func (c *GitHub) Host() string { return c.host }

// TokenSource is where the token came from ("option", or go-gh's
// GH_TOKEN / GITHUB_TOKEN / oauth_token / keyring / ...), for
// `config check`.
func (c *GitHub) TokenSource() string { return c.tokenSource }

// LastRate is the GraphQL budget from the latest list-shaped query.
func (c *GitHub) LastRate() *model.RateLimit { return c.lastRate.Load() }

func (c *GitHub) setRate(r *model.RateLimit) {
	if r != nil && r.Limit > 0 {
		c.lastRate.Store(r)
	}
}

// ---- transport ---------------------------------------------------------

const (
	acceptJSON = "application/vnd.github+json"
	acceptDiff = "application/vnd.github.v3.diff"
)

// restURL resolves a path (or an absolute URL) against the REST base.
func (c *GitHub) restURL(path string) string {
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		return path
	}
	return c.restBase + strings.TrimLeft(path, "/")
}

// do performs one request and returns the body, the response headers
// and status. Every failure is an *Error: HTTP errors carry the status
// and GitHub's message (plus RetryAt on a rate-limited 403/429), a
// transport failure is "network error: ...".
func (c *GitHub) do(ctx context.Context, method, rawURL string, payload any, accept string) ([]byte, http.Header, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, &Error{Method: method, URL: rawURL, Message: "bad request body: " + err.Error()}
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, nil, &Error{Method: method, URL: rawURL, Message: "bad request: " + err.Error()}
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, &Error{Method: method, URL: rawURL, Message: "network error: " + netErrorText(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, httpError(method, rawURL, resp)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		// a timeout or a dropped connection while reading the response
		return nil, resp.Header, &Error{Method: method, URL: rawURL, Message: "network error: " + netErrorText(err)}
	}
	return out, resp.Header, nil
}

// httpError turns a non-2xx response into an *Error, reading GitHub's
// message from the JSON body when there is one. go-gh's HandleHTTPError
// does the parsing.
func httpError(method, rawURL string, resp *http.Response) *Error {
	e := &Error{Method: method, URL: rawURL, Status: resp.StatusCode, Message: resp.Status}
	if herr, ok := api.HandleHTTPError(resp).(*api.HTTPError); ok && herr.Message != "" {
		e.Message = herr.Message
	}
	// 403/429 with rate-limit headers: record when to retry so the caller
	// backs off instead of hammering the wall every tick
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if at, ok := RetryAtFromHeaders(resp.Header, time.Now()); ok {
			e.RetryAt = at
		}
	}
	return e
}

func netErrorText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timed out"
		}
		return ue.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	s := err.Error()
	if s == "" {
		return fmt.Sprintf("%T", err)
	}
	return s
}

// restJSON performs a REST call and decodes the JSON body into out
// (nil to discard). An empty body is fine when out is nil.
func (c *GitHub) restJSON(ctx context.Context, method, path string, payload, out any) error {
	rawURL := c.restURL(path)
	body, _, err := c.do(ctx, method, rawURL, payload, acceptJSON)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Method: method, URL: rawURL,
			Message: fmt.Sprintf("bad response from %s %s: %v", method, rawURL, err)}
	}
	return nil
}

// restRaw performs a GET with a custom Accept and returns the body as
// text.
func (c *GitHub) restRaw(ctx context.Context, path, accept string) (string, error) {
	body, _, err := c.do(ctx, http.MethodGet, c.restURL(path), nil, accept)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// GQLError is one entry of a GraphQL response's errors[].
type GQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Path    []any  `json:"path"`
}

// pathIs reports whether the error's path starts with the given keys.
func (e GQLError) pathIs(keys ...string) bool {
	if len(e.Path) < len(keys) {
		return false
	}
	for i, k := range keys {
		s, ok := e.Path[i].(string)
		if !ok || s != k {
			return false
		}
	}
	return true
}

// gqlData is a GraphQL response's data as one raw message per top-level
// field (an alias, "repository", "rateLimit"...).
type gqlData map[string]json.RawMessage

// get decodes one top-level field into out; ok is false when the field
// is absent or null.
func (d gqlData) get(key string, out any) (bool, error) {
	raw, ok := d[key]
	if !ok || isNull(raw) {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, err
	}
	return true, nil
}

func isNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// graphql posts a query and returns (data, errors). GraphQL can return
// partial data alongside errors (e.g. the token can't see commits/checks
// but can see the PRs): use what we got; only a fully failed query is
// an error. GraphQL rate limiting arrives as HTTP 200 + a RATE_LIMITED
// error: its RetryAt comes from the response headers, or now+60s when
// they are absent, so we still stop hammering.
func (c *GitHub) graphql(ctx context.Context, query string, variables map[string]any) (gqlData, []GQLError, error) {
	rawURL := c.graphqlURL
	payload := map[string]any{"query": query, "variables": variables}
	body, hdrs, err := c.do(ctx, http.MethodPost, rawURL, payload, acceptJSON)
	if err != nil {
		return nil, nil, err
	}
	var out struct {
		Data   gqlData    `json:"data"`
		Errors []GQLError `json:"errors"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, nil, &Error{Method: http.MethodPost, URL: rawURL,
			Message: fmt.Sprintf("bad response from POST %s: %v", rawURL, err)}
	}
	if len(out.Data) == 0 {
		e := &Error{Method: http.MethodPost, URL: rawURL, Message: "GraphQL error"}
		if len(out.Errors) > 0 {
			e0 := out.Errors[0]
			if e0.Message != "" {
				e.Message = e0.Message
			}
			if e0.Type == "RATE_LIMITED" || strings.Contains(strings.ToLower(e0.Message), "rate limit") {
				now := time.Now()
				if at, ok := RetryAtFromHeaders(hdrs, now); ok {
					e.RetryAt = at
				} else {
					e.RetryAt = now.Add(60 * time.Second)
				}
			}
		}
		return nil, out.Errors, e
	}
	if raw, ok := out.Data["rateLimit"]; ok && !isNull(raw) {
		var rl model.RateLimit
		if json.Unmarshal(raw, &rl) == nil {
			c.setRate(&rl)
		}
	}
	return out.Data, out.Errors, nil
}

// repoNode decodes data[key] (a repository(...) field) into out. A null
// repository means a repo typo or a token that can't see it — not
// "nothing open".
func repoNode(data gqlData, errs []GQLError, key string, out any) error {
	ok, err := data.get(key, out)
	if err != nil {
		return &Error{Message: "bad response: " + err.Error()}
	}
	if !ok {
		msg := "not found"
		for _, e := range errs {
			if (len(e.Path) == 0 || e.pathIs(key)) && e.Message != "" {
				msg = e.Message
				break
			}
		}
		return &Error{Message: "repository not visible to this token: " + msg}
	}
	return nil
}

func repoVars(repo model.Repo) map[string]any {
	return map[string]any{"owner": repo.Owner, "name": repo.Name}
}

func repoPath(repo model.Repo, rest string) string {
	return "repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/" + rest
}
