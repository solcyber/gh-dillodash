package ghapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// RepoRefusedError is a write refused before it reached GitHub: the repo
// is not in [github] allowed_repos.
type RepoRefusedError struct {
	Repo model.Repo
	Op   string // what was refused: "comment", "merge", "label", ...
}

func (e *RepoRefusedError) Error() string {
	return fmt.Sprintf("%s not sent: %s is not in [github] allowed_repos, so this dashboard is read-only there", e.Op, e.Repo)
}

// WithAllowedRepos wraps c so that every write to a repo outside
// allowed is refused with a *RepoRefusedError and never sent; reads pass
// through. An empty allowed returns c unchanged. It is the backstop
// under every key that posts: a check in one action can be forgotten,
// the client cannot be gone around.
func WithAllowedRepos(c Client, allowed []model.Repo) Client {
	if len(allowed) == 0 {
		return c
	}
	set := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		set[strings.ToLower(r.String())] = true
	}
	return &repoGuard{Client: c, allowed: set}
}

// repoGuard is a Client whose writes are limited to the allowed repos.
type repoGuard struct {
	Client
	allowed map[string]bool
}

var _ Client = (*repoGuard)(nil)

func (g *repoGuard) check(repo model.Repo, op string) error {
	if g.allowed[strings.ToLower(repo.String())] {
		return nil
	}
	return &RepoRefusedError{Repo: repo, Op: op}
}

// TokenSource and Host forward to the wrapped client when it has them
// (the bot-login hint and the header read them).
func (g *repoGuard) TokenSource() string {
	if s, ok := g.Client.(interface{ TokenSource() string }); ok {
		return s.TokenSource()
	}
	return ""
}

func (g *repoGuard) Host() string {
	if h, ok := g.Client.(interface{ Host() string }); ok {
		return h.Host()
	}
	return ""
}

func (g *repoGuard) Approve(ctx context.Context, repo model.Repo, number int, body, headSHA string) error {
	if err := g.check(repo, "review approval"); err != nil {
		return err
	}
	return g.Client.Approve(ctx, repo, number, body, headSHA)
}

func (g *repoGuard) Comment(ctx context.Context, repo model.Repo, number int, body string) error {
	if err := g.check(repo, "comment"); err != nil {
		return err
	}
	return g.Client.Comment(ctx, repo, number, body)
}

func (g *repoGuard) PostComment(ctx context.Context, repo model.Repo, number int, body string) (int64, error) {
	if err := g.check(repo, "comment"); err != nil {
		return 0, err
	}
	return g.Client.PostComment(ctx, repo, number, body)
}

func (g *repoGuard) Dispatch(ctx context.Context, repo model.Repo, eventType string, payload map[string]any) error {
	if err := g.check(repo, "repository dispatch"); err != nil {
		return err
	}
	return g.Client.Dispatch(ctx, repo, eventType, payload)
}

func (g *repoGuard) MergePR(ctx context.Context, repo model.Repo, number int, sha, title, method string) (MergeResult, error) {
	if err := g.check(repo, "merge"); err != nil {
		return MergeResult{}, err
	}
	return g.Client.MergePR(ctx, repo, number, sha, title, method)
}

func (g *repoGuard) RequestChanges(ctx context.Context, repo model.Repo, number int, body string) error {
	if err := g.check(repo, "review"); err != nil {
		return err
	}
	return g.Client.RequestChanges(ctx, repo, number, body)
}

func (g *repoGuard) DismissReview(ctx context.Context, repo model.Repo, number int, reviewID int64, message string) error {
	if err := g.check(repo, "review dismissal"); err != nil {
		return err
	}
	return g.Client.DismissReview(ctx, repo, number, reviewID, message)
}

func (g *repoGuard) DispatchWorkflow(ctx context.Context, repo model.Repo, file, ref string, inputs map[string]any) error {
	if err := g.check(repo, "workflow run"); err != nil {
		return err
	}
	return g.Client.DispatchWorkflow(ctx, repo, file, ref, inputs)
}

func (g *repoGuard) ApproveDeployments(ctx context.Context, repo model.Repo, runID int64, envIDs []int64, comment string) error {
	if err := g.check(repo, "deployment approval"); err != nil {
		return err
	}
	return g.Client.ApproveDeployments(ctx, repo, runID, envIDs, comment)
}

func (g *repoGuard) RejectDeployments(ctx context.Context, repo model.Repo, runID int64, envIDs []int64, comment string) error {
	if err := g.check(repo, "deployment rejection"); err != nil {
		return err
	}
	return g.Client.RejectDeployments(ctx, repo, runID, envIDs, comment)
}

func (g *repoGuard) RerunRun(ctx context.Context, repo model.Repo, runID int64) error {
	if err := g.check(repo, "re-run"); err != nil {
		return err
	}
	return g.Client.RerunRun(ctx, repo, runID)
}

func (g *repoGuard) RerunFailedJobs(ctx context.Context, repo model.Repo, runID int64) error {
	if err := g.check(repo, "re-run"); err != nil {
		return err
	}
	return g.Client.RerunFailedJobs(ctx, repo, runID)
}

func (g *repoGuard) CancelRun(ctx context.Context, repo model.Repo, runID int64) error {
	if err := g.check(repo, "cancel"); err != nil {
		return err
	}
	return g.Client.CancelRun(ctx, repo, runID)
}

func (g *repoGuard) AddLabels(ctx context.Context, repo model.Repo, number int, labels []string) error {
	if err := g.check(repo, "label"); err != nil {
		return err
	}
	return g.Client.AddLabels(ctx, repo, number, labels)
}

func (g *repoGuard) RemoveLabel(ctx context.Context, repo model.Repo, number int, label string) error {
	if err := g.check(repo, "label"); err != nil {
		return err
	}
	return g.Client.RemoveLabel(ctx, repo, number, label)
}

func (g *repoGuard) AddAssignees(ctx context.Context, repo model.Repo, number int, logins []string) error {
	if err := g.check(repo, "assignment"); err != nil {
		return err
	}
	return g.Client.AddAssignees(ctx, repo, number, logins)
}

func (g *repoGuard) RemoveAssignees(ctx context.Context, repo model.Repo, number int, logins []string) error {
	if err := g.check(repo, "assignment"); err != nil {
		return err
	}
	return g.Client.RemoveAssignees(ctx, repo, number, logins)
}

func (g *repoGuard) SetIssueState(ctx context.Context, repo model.Repo, number int, state, reason string) error {
	if err := g.check(repo, "state change"); err != nil {
		return err
	}
	return g.Client.SetIssueState(ctx, repo, number, state, reason)
}

func (g *repoGuard) CreateBranch(ctx context.Context, repo model.Repo, branch, sha string) error {
	if err := g.check(repo, "creating a branch"); err != nil {
		return err
	}
	return g.Client.CreateBranch(ctx, repo, branch, sha)
}

func (g *repoGuard) DeleteRef(ctx context.Context, repo model.Repo, ref string) error {
	if err := g.check(repo, "deleting a ref"); err != nil {
		return err
	}
	return g.Client.DeleteRef(ctx, repo, ref)
}

func (g *repoGuard) CommitOnBranch(ctx context.Context, repo model.Repo, branch, expectedHead, headline, body string, adds []model.FileAdd, dels []string) (string, error) {
	if err := g.check(repo, "committing"); err != nil {
		return "", err
	}
	return g.Client.CommitOnBranch(ctx, repo, branch, expectedHead, headline, body, adds, dels)
}

func (g *repoGuard) CreatePR(ctx context.Context, repo model.Repo, base, head, title, body string) (int, error) {
	if err := g.check(repo, "opening a pull request"); err != nil {
		return 0, err
	}
	return g.Client.CreatePR(ctx, repo, base, head, title, body)
}

func (g *repoGuard) CreateTag(ctx context.Context, repo model.Repo, name, message, sha string) error {
	if err := g.check(repo, "tagging"); err != nil {
		return err
	}
	return g.Client.CreateTag(ctx, repo, name, message, sha)
}
