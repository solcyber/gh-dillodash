# Authentication and permissions

dillodash is a `gh` extension: it resolves the token exactly the way the
GitHub CLI does. `GH_TOKEN` or `GITHUB_TOKEN` in the environment
(`GH_ENTERPRISE_TOKEN` or `GITHUB_ENTERPRISE_TOKEN` for a GitHub
Enterprise Server host) wins; otherwise it is the account `gh auth login`
stored (`gh auth status` shows it, `gh auth switch` changes it). It never
reads a token from its config or from a file, and it never writes one.
`gh dillodash config check` prints where the token came from, warns when
an environment variable is overriding the `gh` login, and probes what the
token can read.

That override is worth knowing on a shared host: a `GITHUB_TOKEN` exported
by direnv in a project directory, by CI, or by a shared shell profile
silently wins over your own login. To run as yourself, unset it or export
your own token: `GH_TOKEN=<your token> gh dillodash`.

The host comes from `[github] host` in the config; when that is unset,
from `$GH_HOST`, then `gh`'s default host, then `github.com`.

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
commit) instead, which `Actions: read` covers; the release PRs of the
`release_pr` workflow go through the same fallback as the review list. A
token that can read the rollup directly, such as a classic token or the
`gh` OAuth token, uses it and never touches the fallback. A repo where the
Actions call is refused (401, 403, 404) is left alone until the next
manual refresh (`r`), which re-tries it; a transient failure keeps the
last known result and is re-tried on the next refresh.

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
waits too rather than hammering the wall. A secondary (abuse) limit,
which GitHub may answer without a reset time, pauses for at least a
minute, as GitHub asks.

## Security notes

- Tokens are per person. Do not share one between approvers: the audit
  trail of who approved is only meaningful when the commenter is the human
  who decided. For the same reason dillodash refuses to start, and
  `config check` fails, when the signed-in login is one of `[bots] logins`
  or ends in `[bot]`: it would post every review, comment and merge as the
  bot. The message names the token source to fix (`unset GH_TOKEN`, or
  `gh auth switch`).
- dillodash does only what the keys say: it posts reviews and comments,
  merges when you press `M`, approves deployments when you press `D`, and
  nothing else. It never tags, pushes, cancels runs or touches branches.
- The config is not a secret. Keep it readable by you anyway; it names
  your repos.
