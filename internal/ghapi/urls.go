package ghapi

import (
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// DefaultHost is github.com.
const DefaultHost = "github.com"

// NormalizeHost lowercases a host and maps "" (and api./gist. prefixes)
// to the canonical hostname.
func NormalizeHost(host string) string {
	if strings.TrimSpace(host) == "" {
		return DefaultHost
	}
	return auth.NormalizeHostname(strings.TrimSpace(host))
}

// WebBase is the browser-facing origin of a host: https://github.com or
// https://<ghes-host>.
func WebBase(host string) string {
	return "https://" + NormalizeHost(host)
}

// RESTBase is where the REST API of a host lives, with a trailing slash:
// https://api.github.com/ for github.com, https://<host>/api/v3/ for
// GHES (the same derivation go-gh uses).
func RESTBase(host string) string {
	host = NormalizeHost(host)
	if auth.IsEnterprise(host) {
		return fmt.Sprintf("https://%s/api/v3/", host)
	}
	return fmt.Sprintf("https://api.%s/", host)
}

// GraphQLURL is the GraphQL endpoint of a host.
func GraphQLURL(host string) string {
	host = NormalizeHost(host)
	if auth.IsEnterprise(host) {
		return fmt.Sprintf("https://%s/api/graphql", host)
	}
	return fmt.Sprintf("https://api.%s/graphql", host)
}

// WebURL is the browser URL of a repo item on host. kind is one of
// "pr", "issue", "compare" (ref "base...head"), "tree" (ref is a
// branch), "commit" (ref is a sha), or "" for the repository itself.
func WebURL(host string, repo model.Repo, kind, ref string) string {
	base := WebBase(host) + "/" + repo.Owner + "/" + repo.Name
	switch kind {
	case "pr", "pull":
		return base + "/pull/" + ref
	case "issue", "issues":
		return base + "/issues/" + ref
	case "compare":
		return base + "/compare/" + ref
	case "tree", "branch":
		return base + "/tree/" + ref
	case "commit":
		return base + "/commit/" + ref
	case "actions":
		return base + "/actions/runs/" + ref
	}
	return base
}

// PRURL is the browser URL of a pull request.
func PRURL(host string, repo model.Repo, number int) string {
	return WebURL(host, repo, "pr", fmt.Sprint(number))
}

// IssueURL is the browser URL of an issue.
func IssueURL(host string, repo model.Repo, number int) string {
	return WebURL(host, repo, "issue", fmt.Sprint(number))
}

// CompareURL is the browser URL of base...head.
func CompareURL(host string, repo model.Repo, base, head string) string {
	return WebURL(host, repo, "compare", base+"..."+head)
}

// TreeURL is the browser URL of a branch.
func TreeURL(host string, repo model.Repo, branch string) string {
	return WebURL(host, repo, "tree", branch)
}

// CommitURL is the browser URL of a commit.
func CommitURL(host string, repo model.Repo, sha string) string {
	return WebURL(host, repo, "commit", sha)
}
