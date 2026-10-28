# Authentication and permissions

dillodash is a `gh` extension: it uses the token the GitHub CLI holds for
the host (`gh auth login`, `gh auth status`, `gh auth switch`). It never
reads a token from its config, from a file or from the environment itself,
and it never writes one. `gh dillodash config check` prints where the token
came from and what it can read.

## What the token must be able to do

GitHub's API does not authenticate with SSH keys; those only cover git push
and pull. A `gh auth login` through the browser gives an OAuth token with
the `repo` scope, which covers everything below. If your organization
requires fine-grained personal access tokens (`gh auth login --with-token`
with one), create it with:

| Setting | Value |
|---|---|
| Resource owner | your organization (not your personal account, or the repos 404) |
| Repository access | every repo in `[reviews] repos` and, for `release_pr`, every release repo |
| Pull requests | **Read and write**: the dashboard, reviews, approval comments |
| Issues | **Read and write**: the issues section, issue comments, tagging, and the window and release commands, which are comments. Without it the dashboard shows PRs only and says so on the warn line |
| Actions | **Read**: the CI column, see below; the deploy state of merged releases |
| Contents | **Read**: commit data and the diff viewers |
| Metadata | Read (added automatically); `release_pr` discovery reads the org's repos and rulesets with it |
| Commit statuses | Read, only if some CI posts classic statuses; harmless to include |

Merging and approving deployments need more, on the repos where you use
`M` and `D`:

| Setting | Value |
|---|---|
| Contents | **Read and write**: `M` merges through GitHub's merge endpoint |
| Deployments | **Read and write**: `D` approves the environment deployments waiting on a merge |

Without them GitHub answers `403 Resource not accessible by personal access
token`, and `M`/`D` name the permission to add.

## Why "Actions" and not "Checks"

GitHub offers no Checks permission for fine-grained tokens (only GitHub
Apps have one), so the GraphQL check rollup is invisible to them. dillodash
detects that and reads CI from the Actions API (workflow runs by head
commit) instead, which `Actions: read` covers. A token that can read the
rollup directly, such as a classic token or the `gh` OAuth token, uses it
and never touches the fallback.

## GitHub Enterprise Server

Set `[github] host = "ghe.example.com"` and sign in to it:
`gh auth login --hostname ghe.example.com`. REST, GraphQL and the browser
URLs follow the host.

## Rate limits

A refresh costs a handful of GraphQL points (one query per group of four
repos) plus, while CI is in flight and the rollup is hidden, one Actions
call per open PR. The default five-minute interval keeps well inside the
hourly budget; the header shows the remaining points as a meter. When
GitHub does rate-limit the token, dillodash pauses refreshing until the
window resets, says so on the status line, and resumes by itself; `r`
waits too rather than hammering the wall.

## Security notes

- Tokens are per person. Do not share one between approvers: the audit
  trail of who approved is only meaningful when the commenter is the human
  who decided.
- dillodash does only what the keys say: it posts reviews and comments,
  merges when you press `M`, approves deployments when you press `D`, and
  nothing else. It never tags, pushes, cancels runs or touches branches.
- The config is not a secret. Keep it readable by you anyway; it names
  your repos.
