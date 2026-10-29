# The `waiting_runs` workflow

For repositories where Actions runs wait on a protected environment's
approval without a pull request behind them: a production deploy someone
dispatched by hand, an ops workflow run from `main`. The core's `D` finds
waiting runs only through a merged PR's merge commit, so these never showed
on the dashboard and the approver had to go to the Actions page. With this
workflow on, every run waiting on an environment in the configured repos
gets a row, and `D` approves it.

```toml
[workflows.waiting_runs]
enabled = true
repos = ["acme/infra", "acme/etl-jobs"]   # default: every [reviews] repo
```

No bot protocol is involved: the rows come from GitHub's own Actions and
deployment APIs.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Turn the workflow on. |
| `repos` | every `[reviews]` repo | The repos whose waiting runs are listed, as `"owner/name"`. A repo need not be a review repo. |
| `approval_comment` | `"dillodash: approved {workflow} run {run} in {repo}"` | The comment GitHub records with each approval. `{repo}` is `owner/name`, `{workflow}` the workflow's name, `{run}` the run id; any other placeholder is a config error. |

## What you see

- **The `waiting approvals` section** sits between the pull requests and
  the releases, one row per waiting run, the one waiting longest first:
  `run`, the repo (when more than one repo is configured or reviewed), a
  chip per environment, the workflow's name, the branch it ran on, who
  started it (the run's `actor`: the dispatcher of a `workflow_dispatch`,
  kept across re-runs), its age, and its title.
- **The chips.** `approve: prod` (gold, the title gold too) is an
  environment you are a required reviewer of; `waits: prod` waits on
  someone else; `waiting` means the environments couldn't be read (the
  warn line says why).
- **The summary row** counts `N runs waiting`, gold with `, M on you (D)`
  when some wait on you. Nothing shows when nothing waits.
- `/` matches the repo, the workflow, the title, the branch, who started
  it, the environments and the run id. `o` opens the run on GitHub, `y`
  copies its URL; Enter only says so (a run has no detail view).

The title is the run's `display_title`: the workflow's `run-name:`, else
the commit headline. The Actions runs API does not return a dispatch's
inputs, so a workflow whose inputs matter to the approver (which service,
which version, which environment) should put them in its `run-name:`:

```yaml
run-name: deploy ${{ inputs.service }} ${{ inputs.version }} to ${{ inputs.environment }}
```

## Runs that belong to a listed PR

A run whose head commit is the head or the merge commit of a PR the
dashboard already lists is left out: a review PR's, or a merged release's
in the `release_pr` section. Those show under their PR, and `D` on that PR
approves them through its merge commit, so a release's deploy is never
listed twice. The review PRs' runs are dropped at refresh (their
environments are not even read); the releases are matched when the list is
drawn. Note that this also leaves out a run waiting on an open PR's head,
which `D` on that PR does not approve (it approves merged PRs only).

## `D`

`D` on a run row reads the run's environments again (someone may have
approved one since the refresh), then:

- when none is yours, says which environments wait and who can approve
  them, as GitHub names the reviewers: `infra deploy run 4242 waits on
  prod, but alice is not a reviewer (prod: bob, team deployers)`;
- otherwise asks `approve 1 deployment of infra deploy run 4242 as
  alice?` with one line per environment of yours (and the ones that are
  not), approves each with `approval_comment`, says what it approved, and
  refreshes.

The approval goes through your own `gh` token, the same as every other
action: GitHub checks that you are a reviewer of the environment.

## Refresh and failures

Every refresh reads one page (50) of each repo's waiting runs
(`GET /repos/{owner}/{repo}/actions/runs?status=waiting`) and the pending
deployments of each run. A repo whose runs can't be read is a warning
naming the permission when GitHub answered 403, and the other repos still
list; a rate limit pauses the workflow with the rest of the dashboard and
keeps the last rows.

## Permissions

On every repo in `repos` the token needs Actions read (the runs and their
pending deployments) and, for `D`, Deployments read and write. See
[auth.md](../auth.md).
