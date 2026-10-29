package ghapi

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// defaultHost is github.com.
const defaultHost = "github.com"

// normalizeHost lowercases a host and maps "" (and api./gist. prefixes)
// to the canonical hostname.
func normalizeHost(host string) string {
	if strings.TrimSpace(host) == "" {
		return defaultHost
	}
	return auth.NormalizeHostname(strings.TrimSpace(host))
}

// WebBase is the browser-facing origin of a host: https://github.com or
// https://<ghes-host>.
func WebBase(host string) string {
	return "https://" + normalizeHost(host)
}

// restBase is where the REST API of a host lives, with a trailing slash:
// https://api.github.com/ for github.com, https://<host>/api/v3/ for
// GHES (the same derivation go-gh uses).
func restBase(host string) string {
	host = normalizeHost(host)
	if auth.IsEnterprise(host) {
		return fmt.Sprintf("https://%s/api/v3/", host)
	}
	return fmt.Sprintf("https://api.%s/", host)
}

// graphQLURL is the GraphQL endpoint of a host.
func graphQLURL(host string) string {
	host = normalizeHost(host)
	if auth.IsEnterprise(host) {
		return fmt.Sprintf("https://%s/api/graphql", host)
	}
	return fmt.Sprintf("https://api.%s/graphql", host)
}

// webURL is the browser URL of a repo item on host. kind is one of
// "pr", "issue", "compare" (ref "base...head"), "tree" (ref is a
// branch), "commit" (ref is a sha), or "" for the repository itself.
// Owner, name and every ref segment are path-escaped: a branch may
// carry a space, a # or a %, and the URL reaches a shell on Windows.
func webURL(host string, repo model.Repo, kind, ref string) string {
	base := WebBase(host) + "/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
	switch kind {
	case "pr", "pull":
		return base + "/pull/" + escapeRef(ref)
	case "issue", "issues":
		return base + "/issues/" + escapeRef(ref)
	case "compare":
		if i := strings.Index(ref, "..."); i >= 0 {
			return base + "/compare/" + escapeRef(ref[:i]) + "..." + escapeRef(ref[i+3:])
		}
		return base + "/compare/" + escapeRef(ref)
	case "tree", "branch":
		return base + "/tree/" + escapeRef(ref)
	case "commit":
		return base + "/commit/" + escapeRef(ref)
	case "actions":
		return base + "/actions/runs/" + escapeRef(ref)
	}
	return base
}

// escapeRef path-escapes a git ref segment by segment, keeping the
// slashes that structure it (feat/a b -> feat/a%20b).
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// PRURL is the browser URL of a pull request.
func PRURL(host string, repo model.Repo, number int) string {
	return webURL(host, repo, "pr", fmt.Sprint(number))
}

// IssueURL is the browser URL of an issue.
func IssueURL(host string, repo model.Repo, number int) string {
	return webURL(host, repo, "issue", fmt.Sprint(number))
}

// CompareURL is the browser URL of base...head.
func CompareURL(host string, repo model.Repo, base, head string) string {
	return webURL(host, repo, "compare", base+"..."+head)
}

// TreeURL is the browser URL of a branch.
func TreeURL(host string, repo model.Repo, branch string) string {
	return webURL(host, repo, "tree", branch)
}

// CommitURL is the browser URL of a commit.
func CommitURL(host string, repo model.Repo, sha string) string {
	return webURL(host, repo, "commit", sha)
}
