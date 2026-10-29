# The `release_pr` workflow

For application repositories that release to production by one path: an
open pull request into the production branch, either `release/<date>` (the
cut on the staging branch) or `hotfix/<slug>`. A human's approving review
is the gate; the merge is the deploy; environments may wait for an
approval before deploying. dillodash is that human's console across every
such repository.

```toml
[workflows.release_pr]
enabled = true
repos = "auto"                  # every repo in `org` a ruleset named `ruleset` applies to
org = "acme"
ruleset = "release"
production_branch = "main"
staging_branch = "stage"
```

Or list the repos explicitly: `repos = ["acme/web-portal", "acme/etl-jobs"]`.
Discovery reads the org's repo list and each non-archived repo's rulesets
once a day (cached under `~/.cache/dillodash/`); a repo appears by itself
the day after it is enrolled, or at once after the cache file is deleted.

## What you see

- **The `releases` section** sits below the pull requests. One row per
  open PR into production, per repo, as `repo#N`: a chip for its review
  state (`needs approval` gold, `approved by <who>`, `approved by me`,
  `changes requested`, `DRAFT`), a kind chip (`release`, or gold
  `hotfix`), CI (read through the same Actions fallback as the review list
  when the token cannot see the check rollup), author, age, title. `A`
  never hides a release: an approved one is in flight until it merges.
- **The `stage:` line** under each repo counts what the staging branch
  carries ahead of production by tag: `stage: 3 units ahead of main · 1 go
  · 1 hold · 1 untagged`. A unit is a first-parent commit of staging that
  is not on production; its PR number is read from the commit subject
  (`Merge pull request #N` or a squash's `(#N)`), and the tags
  `go_tag_prefix<N>` / `hold_tag_prefix<N>` decide the count. Untagged
  counts as hold: a unit nobody tagged does not release. `enter` does
  nothing on this line; `o` and `y` give its compare page.
- **A PR into production that is not a release** (its branch has neither
  prefix) is listed last in its repo, dimmed, with the chip `not a
  release`. It is not counted, and `a`, `M` and `D` refuse it. Set
  `list_other_prs = false` to hide them.
- **A queued release** (with `maintenance_window` enabled and the
  conductor's queued label on it) shows under its night in the window
  section; its manifest item id is `rel:<repo>#<N>`.
- **A merged release** stays listed for `merged_keep_secs` (12 hours) and
  shows its deploy in the second chip: `approve: prod` (an environment you
  review waits), `waits: prod` (someone else's), `deploying`, `deployed`,
  `deploy failed`, `deploy cancelled`, `stuck? 1h05m` (a run queued or
  running longer than `deploy_stuck_secs`, 45 minutes by default), or `no
  Actions runs`. The card lists every run.
- **The summary row** counts `N releases need approval`, `N deploys wait
  on you (D)` and `N deploys stuck or failed`.

## `a` on a release

Open it with `enter` first. `a` refuses drafts, your own PRs, merged ones
and a PR that is not a release, then offers:

- **queue it for the window (approve review)**, when
  `approval_queues_for_window = true` and the window workflow is enabled:
  posts the approving review and nothing else; the conductor queues it.
- **release NOW** (`release_now_command`, default `/release-now`): asks for
  a reason of at least `release_now_min_reason` characters with no double
  quotes, shows the exact comment, then posts the review **first** and
  `/release-now "<reason>"` second. If the comment fails after the review
  landed, the status line says the release is approved but the command
  was not posted.
- A **hotfix** lists NOW first (enter picks it): a hotfix into production
  is an emergency by definition.

Without the window workflow, an approving review is just an approval, and
`M` merges whenever GitHub allows.

## `M` and `D`

- `M` on an approved release merges it with `merge_method` (a merge commit
  by default), naming the head so a branch that moved after the approval
  is refused by GitHub. It refuses an unapproved, draft, blocked or already
  merged PR. With the window workflow on, inside `release_from`–`end` it
  asks once ("This DEPLOYS production"); outside the window it first asks
  you to call it an emergency (the `/release-now` reason belongs on the PR).
- `D` on a merged release (its row, or its detail) approves the environment
  deployments waiting on its merge commit, and nothing else: only runs
  whose head is that merge, only environments you are a reviewer of. With
  nothing to approve it says what the deploy is doing instead. The
  approval carries `deploy_approval_comment`.
- While a deploy waits or runs dillodash refreshes every
  `deploy_refresh_secs` and says once per environment when one starts
  waiting for you (`web-portal#143: prod now waits for your approval -
  press D on its row`). Approve `stg`, wait for the message, press `D`
  again for `prod`.

## Permissions

On every release repo the token needs Pull requests read and write (the
review), Issues read and write (the `/release-now` comment), Contents read,
Actions read, Metadata read (discovery), and for `M` and `D` Contents and
Deployments read and write. A repo the token cannot see shrinks the
section and puts the reason on the warn line; a repo whose read fails keeps
its last good rows with a warning.
