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
- **A queued release** shows under its night in the window section, not
  among the releases; its manifest item id is `rel:<repo>#<N>`. With
  `approval_queues_for_window = true` the approving review itself queues
  it (an open, non-draft `release/*` PR with an APPROVED review no older
  than three nights, a review with no recorded time counting as fresh; a
  hotfix never queues, and its `a` menu does not offer it), which is what
  the conductor's freeze picks up: it labels nothing before then. A
  release carrying the conductor's queued label is queued either way.
  Once the release is on its way (released NOW, or its slot came) it is
  back among the releases with its rail; one that ended without merging
  (a failure, a refusal) and still holds its approval goes back under its
  night, as the conductor would take it again.
- **A release in flight** (released NOW, or on tonight's manifest) shows
  its **rail** in the kind chip: a meter that fills as the release moves
  through the manifest repo's run, with the phase and its clock
  (`▰▰▰▰▱▱▱ deploying 3/9 · 2m`, `scheduled 16:00`, `rolled · re-cut`, `released`, `release
  FAILED`). See [Live progress](#live-progress).
- **A merged release** stays listed for `merged_keep_secs` (12 hours) and,
  when nothing is following it, shows its deploy in the second chip: `approve: prod` (an environment you
  review waits), `waits: prod` (someone else's), `deploying`, `deployed`,
  `deploy failed`, `deploy cancelled`, `stuck? 1h05m` (a run queued or
  running longer than `deploy_stuck_secs`, 45 minutes by default), or `no
  Actions runs`. The card lists every run.
- **The summary row** counts `N releases need approval`, `N deploys wait
  on you (D)` and `N deploys stuck or failed`.

## `a` on a release

![a on a release: queue it for tonight's window, or release NOW with a reason](../shots/approve-menu.png)

![The release-now prompt: the reason, its length meter, and the exact comment that will be posted](../shots/release-now-prompt.png)

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
  was not posted. With the window workflow on and `release_now_dispatch`
  (default true), it then fires a `repository_dispatch` on the window's
  `manifest_repo`, event `release_now_dispatch_event` (default
  `release-now`), payload `{repo, pr, comment_id}`: the comment lives in
  the release repo, whose events never reach the manifest repo's
  workflows, so this is how the conductor there learns to merge now. The
  payload only points at the comment; the receiver re-reads it by id. If
  the dispatch fails after the comment landed, the status line says the
  manifest repo was not told and points at `M`.
- A **hotfix** lists NOW first (enter picks it): a hotfix into production
  is an emergency by definition.
- A release **you already approved for this head** is queued already
  (where approvals queue), so `a` offers only **release NOW**: the same
  reason prompt and exact comment, then `/release-now "<reason>"` and the
  dispatch, with **no second review** (yours stands). This is the path for
  an approved release the window bumped or rolled. Once the release is on
  its way, `a` says so and posts nothing. Before 1.5.3 the core refused `a`
  on any PR you had approved, so an approved release could only be
  released now by posting the comment and firing the dispatch by hand.
- **Request changes**, always the last entry: a request-changes review
  with a reason of at least ten characters. The conductor's freeze skips a
  release that is not approved, so this is how a release comes out of
  tonight's window before the freeze, with no comment on the manifest.
- **Dismiss my approval**, after NOW on a release you approved: dismisses
  your approving review (by its id, read from the row or the PR) with a
  message. Withdrawn, the release leaves the queue; `a` approves it again,
  and so queues it again, whenever it should go (after a `/window-cancel`,
  say).
- **A command nobody picked up.** A `/release-now` posted from the PR page
  fires no dispatch, and a dispatch can fail. When the command has waited
  two minutes with no run (at once when this session's dispatch failed),
  `a` on the release offers one thing: fire the `release-now` dispatch for
  **that** comment (its id is the payload), with no second comment and no
  review. `M` stays the last resort after five minutes.
- **From anywhere.** The same menu comes up whether the release was
  opened from its row under `releases`, from its row under a night
  (scheduled, bumped or rolled), from `:` (`repo#N` reaches the enrolled
  release repos too, not only the review repos), or from a link in the
  manifest. When no row carries the release (the state has not loaded,
  or a rate-limited refresh stopped early), `a` tells a release by its
  repo and branches: open into `production_branch` of an enrolled repo
  from a `release_prefix` or `hotfix_prefix` head. Until the enrolled
  repos are known (discovery has not run yet), `a` says `couldn't read
  release state … press r and retry` and posts nothing: a release never
  gets a plain review, and never the core's "a second review changes
  nothing". A release whose thread quotes a plan digest is still a
  release, not a plan.
- **Bumped.** A release the freeze left out of tonight (the manifest
  item's `bumped` flag) ends its rail as `bumped`; a bumped or rolled
  release you approved says `bumped — a: release NOW` on its chip, and
  its card's `a:` line says the same.

Without the window workflow, an approving review is just an approval, and
`M` merges whenever GitHub allows.

## `M` and `D`

- `M` on an approved release merges it with `merge_method` (a merge commit
  by default), naming the head so a branch that moved after the approval
  is refused by GitHub. It refuses an unapproved, draft, blocked or already
  merged PR. With the window workflow on, inside `release_from`–`end` it
  asks once ("This DEPLOYS production"). Outside the window, while
  `release_now_dispatch` can tell the manifest repo, `M` refuses and points
  at `a`: release NOW posts the command and fires the dispatch, and the
  release job merges. Merging by hand is the last resort, for when the
  dispatch can't fire (it failed this session, the command has waited
  five minutes with no run, or `release_now_dispatch` is off); then `M` first asks you to call it an emergency (the
  `/release-now` reason belongs on the PR).
- `D` on a merged release (its row, or its detail) approves the environment
  deployments waiting on its merge commit, and nothing else: only runs
  whose head is that merge, only environments you are a reviewer of. With
  nothing to approve it says what the deploy is doing instead. The
  approval carries `deploy_approval_comment`.
- `x` on a release (its row, or its detail) lists its runs: the release
  run in the manifest repo, then the runs on its merge (or head) commit.
  Pick one to re-run its failed jobs or every job, cancel it, reject the
  deployment it waits on (a second choice with its own prompt and confirm;
  `D` only ever approves), or read a job's log in `$PAGER`. Every write
  asks first.
- While a deploy waits or runs dillodash refreshes every
  `deploy_refresh_secs` and says once per environment when one starts
  waiting for you (`web-portal#143: prod now waits for your approval —
  press D on its row`). Approve `stg`, wait for the message, press `D`
  again for `prod`.

## A manual release night

Everything an approver does on a night of manual releases happens in
dillodash, with no GitHub page and no CLI. `gh dillodash --demo-night`
replays one offline; these shots are its steps.

![A release night after the freeze: the approved release the window bumped says a: release NOW, the migration holds its slot nine minutes into the window](../shots/night-1-bumped.png)

- **`a` on a release row** in the list is the detail's `a`: no need to
  open it first. A release the freeze bumped keeps its approval, so `a`
  offers release NOW alone.

![a on the bumped release: the approval stands, so release NOW is the one choice](../shots/night-2-now.png)

- **Identity before posting.** With `[github] approvers_team =
  "org/team"`, dillodash checks that the login it posts as is an active
  member before it posts anything a bot accepts only from an approver: the
  approving review that queues a release, `/release-now`, the window's
  `/attend`, `/release` and `/window-cancel`, and `/approve`. A bot login
  is always refused, team or not. The refusal names the login, and when
  the token came from `GH_TOKEN` or `GITHUB_TOKEN`, the variable to unset
  (a token exported for something else in that shell is the usual way to
  post as the wrong account). When the token cannot read the team (a
  fine-grained token without the organization's Members read), dillodash
  warns and posts; `config check` says which.
- **The lane.** `lane_order` lists the release repos in the order a
  manual night releases them. When a release in the lane lands, its
  message names the next approved release in the lane (`next in lane:
  <repo>#<n> — a on its row: release NOW`), or says to wait while its own
  deploys are still running. NOW on a release while another in the lane
  is still on its way asks first.

![The release lands; the status line names the next release in the lane](../shots/night-3-released.png)

- **A slot on the manifest.** release NOW on a release that holds a slot
  on a manifest (the scheduler's `scheduled` flag, not yet run) asks, in
  the same flow, whether to post `/window-cancel rel:<repo>#<n>` there, so
  the window does not reach a release that is merged already.

![release NOW on the migration that holds a slot: the same flow offers the window-cancel of its item](../shots/night-4-cancel.png)

- **Environments.** A release whose deploy waits on an environment ends
  as `deploy waiting · D`, and its message names the environment. `D` on
  its row approves it; the next one (`prod` after `stg`) is offered the
  same way.
- **The countdown.** With `apply_budget_mins` set, a release in flight
  counts down on its row (`deploying · 23m left`, then `over by 4m`).

![The migration merged and waits on stg: D on its row approves it](../shots/night-5-stg.png)

![stg, then prod: both releases out, with no keystroke outside dillodash](../shots/night-6-done.png)

## Live progress

![A release in flight on the list: its rail is a pip per phase, filled as it goes, with the phase and its clock](../shots/release-rail.png)

![Mission control for a release in flight: the T+ clock, a rail of one segment per phase, one row per phase with its clock and its technical tail, the run](../shots/release-card.png)

![When a release lands: the rocket, the fireworks and RELEASED! across the screen](../shots/release-launch.png)

From the moment a release is sent on its way, dillodash follows it to its
outcome and shows where it stands, in near real time, without waiting for
the next refresh.

**What it watches.** The conductor's release job runs in the window's
`manifest_repo` as one run of `apply_workflow` (default `apply.yml`), fired
by a `repository_dispatch`; its title names the release
(`apply_run_match`, default `rel:{repo}#{number}`). The run's steps say the
phase: *setup*, *validating*, *merging*, *approving envs*, *deploying*,
*verifying* (the alarms are read after a five-minute settle), *finishing*. On the
release PR the job leaves an outcome comment and the conductor's labels
(`applied_label`, `failed_label`, `pending_label`). dillodash reads these
headings, each followed by ` (<how>)`: `## Released`, `## Release NOT
RUN`, `## Release FAILED: merge refused`, `## Release merged; the deploy
is WAITING on an environment approval`, `## Release FAILED: the deploy did
not succeed`, `## Release UNVERIFIABLE`, and `## Release FAILED
VERIFICATION, NOT reverted`. Any other heading is not an outcome. On a window night the
manifest item's status says *scheduled*, *waiting for slot* (terraform
items first, or `/release`), *dispatched*. The app repo's own deploy runs
on the merge give the `n/m` while deploying. The newest signal wins, so a
`/release-now` posted again after a failed run reopens the story with the
new run.

**What you see.**

- **The row's rail**: one pip per run phase, filled blue for a phase
  done, gold for the one running (it breathes while the phase runs, and a
  sweep of light crosses the rail when the phase changes), a thin line for
  the phases to come, a solid gold block where a release failed, every pip
  filled once released. Next to it the phase and its clock, and a spinner
  (a rolling ball while a dispatch waits for its run). A scheduled release
  shows its slot and no spinner. The chip says how it ended: `✓ released`
  pale blue, `✗ release FAILED` dark gold, `deploy waiting · D` and
  `bumped — a: release NOW` bold gold (yours to act on).
- **The detail's mission control**, in the release card: the state chip,
  the run and attempt (or the night and slot), a bold `T+` clock since the
  command (`T-` to the slot) with the apply budget's countdown after it,
  the rail across the card as a stepper (one segment per phase, filling
  within the running one: deploying by runs done, verifying by the
  settle), Amos walking above it on a screen of 32 rows or more (the
  splash's Amos, small, in his own colors), one row per phase with its
  glyph and clock and a technical tail (the merge sha, `n/m runs`, the
  settle countdown, the approver; the running step bold), the outcome and
  the merge once it ends, the run's URL, and the last poll error if there
  was one. The card's rule line comes after the steps while a release is in
  flight, so every step fits at 24 rows. It redraws every frame while
  live.
- **One message when it ends**: `web-portal#90 released — verified on
  9f8e7d6 after 6m02s`, with the launch; `release FAILED at verifying —
  <detail>; run: <url>`; `release refused — <reason>`; a notice for
  *rolled*, *not run*, *cancelled* and *lost track*.
- **A deploy held at an environment.** When the merge is done but the
  deploy run waits on a required-reviewer environment (the `WAITING`
  heading, or `pending_label`), the release ends as **`deploy waiting ·
  D`** in bold gold: the rail stops at *deploying*, the card
  says `D` approves the waiting environment (if you review it) and that
  `window:pending` is swapped for `window:applied` by hand, and one notice
  names the environment. Once someone swaps the label, the row follows it:
  `released` for `applied_label`, `release FAILED` for `failed_label`.

**How often.** Every `progress_poll_secs` (30 s) while something is in
flight: one listing of the apply runs when a release has no run yet, the
jobs of each run in progress, the comments of each release waiting for its
outcome, and nothing at all when nothing is in flight (a scheduled release
is polled from ten minutes before its slot). The whole-dashboard refresh is
untouched. A release with no outcome after `progress_timeout_secs` (90
minutes) is marked *lost track* and left alone. With `progress_poll_secs =
0` nothing is followed and the chips read as before.

**Without the manifest repo.** A token that cannot read the manifest
repo's Actions turns that half off for the session, with one notice; the
PR's labels, comments and deploy runs still carry the release to its
outcome. Set `apply_workflow = ""` to skip the runs on purpose.

**ASCII and motion off.** The rail draws with `#` and `-`, Amos in line
art; with `motion = "off"` nothing breathes or
sweeps and the card still redraws on every poll.

## Permissions

On every release repo the token needs Pull requests read and write (the
review), Issues read and write (the `/release-now` comment), Contents read,
Actions read, Metadata read (discovery), and for `M` and `D` Contents and
Deployments read and write; for `x`, Actions read and write. The release-now dispatch needs Contents read
and write on the window's `manifest_repo` (a `repository_dispatch` is a
contents write there); following the release there needs Actions read on
it (the run's jobs and steps). With `[github] approvers_team`, organization
Members read lets dillodash check the posting login is in the team before
`/release-now` goes out; without it dillodash warns and posts unchecked
(the bot still refuses a non-member). A repo the token cannot see shrinks the
section and puts the reason on the warn line; a repo whose read fails keeps
its last good rows with a warning.
