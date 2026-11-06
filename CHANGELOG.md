# Changelog

## [Unreleased]

### Added

- **Stuck approvals.** An approved PR that will not merge by itself no
  longer drops off the list. My approval of an older commit (the head
  moved after it) or one that was dismissed reads `re-review`, counts as
  needing me, and the row says `stale: approved 1a2b3c4, head 5d6e7f8
  (+2)`. Someone else's stale approval, a conflict with the base, a
  failed check that blocks the merge, or a merge blocked by a rule reads
  `approved · stuck` with the reason; such a PR stays listed even while
  approved-by-others PRs are hidden. On my own PR a conflict or a red
  check counts as needing me. The detail repeats the reason.
- **`u` opens the changes since a stale approval**: the compare view from
  the approved commit to the head, so a re-approve is a look at the
  delta. The bar shows it while the selected PR's approval is stale.
- **`[reviews] requested_repos`**: busy repos listed only for the open
  PRs that ask for your review (directly or through a team), that you
  reviewed, or that you opened, with none of their issues or other
  traffic. A PR you approved stays listed, so a stale approval there
  comes back as `re-review`. `config check` counts them.
- Release rows: my stale approval reads `needs approval` again, and
  someone else's reads `approved by <login> · stale`.

### Fixed

- `a` no longer refuses a re-approval with "already approved by you"
  after the head moved: an approval counts only for the commit it was
  given on.
- `M` in the detail refuses a PR GitHub says cannot merge (a conflict, a
  blocked merge): the detail query now reads the merge state the check
  needs, which it had silently skipped.
- List rows carry their base branch, so a `release/*` or `hotfix/*` head
  into a branch other than production is no longer taken for a release.
- The screenshot tool bounds each 24-bit colour channel when it paints.

## [1.10.0]

Issue triage from the detail view, a hand-off to an agent, and a
dashboard that can be held to the repos and the team it is for.

### Added

- **Triage keys** in the detail view. `L` adds one of the repo's labels or
  removes one the item carries (the menu marks them `[x]`). On an issue,
  `s` assigns a repo user or unassigns an assignee, and `Z` closes it as
  completed or as not planned, or reopens it after a confirm. The detail
  updates at once; the next refresh brings GitHub's word.
- **`H` hands the open PR or issue to an agent**: it adds `[agent]
  handoff_label` and, when `handoff_comment` is set, posts it, after a
  confirm. An agent that is a GitHub App's bot cannot be @-mentioned into
  a notification, so it watches the label. The bar shows `H` only while a
  label is configured.
- **`[github] allowed_repos`**: the only repos dillodash writes to. Every
  comment, review, merge, command, label, assignment, close or reopen,
  deployment approval or rejection, re-run, cancel and dispatch to any
  other repo is refused before a prompt, and again in the GitHub client
  underneath, so no key goes around it, including an item opened with `:`.
  Reading is unaffected. A test fails when a new write is added to the
  client without the guard. A workflow configured to write to a repo
  outside the list (`terraform_approve`, `waiting_runs`, a
  `[[workflows.dispatch]]`, ...) is a warning at load and in `config check`.
- **`[github] approvers_team_strict = true`** refuses a command a bot
  accepts only from an approver when the team cannot be read (a token
  without the organization's Members read, or a failed check), instead of
  warning and posting unchecked.
- **`[display] profile`**: a short badge in the header, for someone who
  runs more than one config.
- `config check` prints `writes: only to ...` when `allowed_repos` is set,
  and says strict mode refuses while the team cannot be read.
- `terraform_approve` reads `Window: **n/a (apply on approval)**` (or
  `**n/a**`) from a repo with no maintenance window: never disruptive, a
  plain `/approve`, the same as a digest with no Window line.

### Fixed

- **Waiting runs on the branch tip are listed.** A `workflow_dispatch`,
  `repository_dispatch` or `schedule` run from the production branch has
  the tip as its head, usually the last merged PR's merge commit, and the
  waiting approvals section left it out as if it were that PR's deploy,
  so a hand-dispatched gated run showed nowhere (#48). Only a `push` run
  on the merge commit of a PR listed in the same repo is left out now.
- **`digest_bot` and `conductor_bot` match exactly.** They matched by
  prefix, so a configured `deploy` also trusted a login `deploy-er`: its
  queue markers, refusals and release outcomes. They now match the login
  or its `[bot]` App form, case-insensitive, like `[bots] logins`.
- **The gallery is reproducible.** The day shots ran on the wall clock
  (the header clock, every age, which night is next) and the spinner on
  the process's age, so every regeneration rewrote every picture. They are
  drawn at a fixed instant now, and a snapshot's spinner turns with its
  clock; a test renders a shot twice and compares.

## [1.9.0]

A room to watch the release window from, a far louder launch, and a
release rail that only believes the release job.

### Added

- **The room.** During the release window the dashboard shrinks into a
  window at the middle of the screen, crossed by its mullions, and the
  rest of the screen is the room you watch it from: lamp-lit wall,
  curtains, plants, a portrait of Amos, a landscape, a wall clock with the
  real time, a mug steaming on the sill. Everything still works through
  the glass. `W` (it is on the plaque under the window) goes back to the
  normal dashboard, and brings the room back; each window starts in the
  room, as a reminder. Outside the window the dashboard stays plain and
  `W` steps into the room any time; the bar offers `W` whenever the room
  fits and is not shown. `[display] room = false` keeps the window plain
  too. The room needs 100x30; a
  smaller terminal gets the normal dashboard. With motion off the room
  holds still.
- **The launch, psychedelic.** From 256 colors up a release lands in a
  kaleidoscope plasma tunnel, with a rainbow rocket on a wiggle, smoke
  rings, a ripple shockwave, spiral-galaxy fireworks, a melting rainbow
  RELEASED! and Amos tumbling through. It runs 8 s; any key still ends it.
  Every color it paints keeps one lightness, so it flows and never
  flashes: a test plays every frame and holds it to the general-flash
  limit (no more than three opposing luminance swings a second over a
  large area). 16 colors and ASCII keep the classic launch.

### Changed

- `:` go to # searches every listed row, release rows included, and asks
  which repo when more than one lists that number. `repo#N` still works.
- A key pressed while an action is still running says so, instead of
  vanishing.

### Fixed

- An outcome comment (`## Released`, `## Failed`, ...) counts only when
  it is the release job's: posted by a bot, with a `Run:` line naming a
  run of the manifest repo. Anyone else's is shown as not believed. A
  release whose ending is known only from a label or a comment draws just
  the outcome, not phases nobody saw run.
- A `/release-now` whose dispatch failed reads `not told · M` in gold,
  with no spinner and no running clock, instead of looking in flight.
  After `M` it reads `merged by hand`.
- `D` in a detail view that was open across the merge re-reads the PR and
  finds the waiting deployment, instead of doing nothing; a failed read
  says why.

## [1.8.0]

The whole release night from the dashboard: review choices, runs,
dispatches and the window's newer rules. Plus the graphics pass and a
keyed hygiene check.

### Added

- `a` on a release offers **request changes**, with a reason: it takes the
  release out of the window. On a release you approved it also offers
  **dismiss my approval**; `a` again re-queues it. Both come after the
  existing entries, so enter and the numbers do what they did.
- A `/release-now` that no run picked up (its dispatch failed, or it was
  posted from the PR page) gets the dispatch for that same comment from
  `a` after two minutes: no second comment, no review.
- `x` acts on the runs of the selected row or the open PR (a release's run,
  a waiting run, the runs on its commit): re-run the failed jobs or every
  job, cancel, reject the deployment it waits on (its own confirm), or read
  a job's log in `$PAGER`. `D` still only approves.
- `X` runs a workflow the config lists under `[[workflows.dispatch]]`
  (`workflow_dispatch` on a file, or a `repository_dispatch` event): typed
  inputs, the exact request in the confirm, then the status line follows
  the new run. Nothing unlisted is ever dispatched.
- `[merge] methods` sets `M`'s merge method per repo.
- `maintenance_window` `reserved_from`, `reserved_to` and `reserved_stacks`:
  inside that span, `a` on a harmless plan to a reserved stack offers to
  wait, or to apply with the outside-window flag and a reason.
- A config installed with `config pull` is compared with its source about
  once an hour; when the repo has a newer one the status line says so, and
  `P` pulls it and restarts into it, keeping your place.
- **Missed freeze.** A night whose freeze never ran (the conductor's
  `missed_freeze_marker` comment, or a manifest still open
  `missed_freeze_grace_mins` after the cutoff) shows `missed freeze` and
  `missed_freeze_hint`; `w` offers no `/attend` or `/release` there.
- **Re-cut after the freeze.** A release whose head moved since the freeze
  recorded it reads `rolled · re-cut` instead of `scheduled`.
- `manifest_title` takes `{hhmm}`, and any `HH:MM` in it matches any time,
  so a night opened under an earlier start time is still found.
- `late_skip` and `closer`: the night line says `closes at …`, then
  `closer late`.
- The hygiene check is keyed: `internal/hygiene/denylist.txt` holds
  HMAC-SHA256 hashes under `DILLODASH_HYGIENE_KEY`, `cmd/gen-denylist`
  regenerates it from a list kept outside the repository, and `cmd/scan`
  checks PR titles, bodies, branch names and commit messages in CI. It
  also reads PNG text chunks and PDF text, phrases and short whole words,
  and reports every hit.
- `cmd/screenshot -tier 256|16|mono` draws a shot at that color depth; new
  shots `tier-16-night` and `tier-mono-list`.

### Changed

- The default window is a labelled example in UTC; set your own times.
- Both demos follow the configured window, and the release-night demo is a
  made-up night.
- The stuck-run line on a release card points at `x` instead of the
  Actions page.
- The legacy migration messages say "legacy config.ini".
- Release chips keep one meaning each: `✓ released` (pale blue) and
  `merged` no longer share the approved chip; `scheduled` sits on steel;
  what you act on now (`deploy waiting · D`, `bumped — a: release NOW`,
  `missed freeze`) is bold gold; a failure (`✗ release FAILED`, `✗ release
  refused`) is a darker gold.
- A release's rail is drawn per run phase: a pip per phase on its row
  (done blue, running gold, a line to come, a gold block where it failed),
  and a stepper of one segment per phase on its card, lined up with the
  steps.
- The release card fits every step at 24 rows: Amos walks the rail from 32
  rows, the rule line follows the steps while a release is in flight, the
  `T+` clock is bold and the apply budget counts down beside it.
- A night's manifest row draws the night's timeline in its title's place:
  the window as a rule, a mark per slot, a gold now mark while it is open.
- The night countdown is blue, gold in its last quarter hour, `live now`
  once the window opens, and stays on a narrow screen.
- Menus, the help and the message box dim the screen behind them; a y/N
  question sits under a gold rule.
- The summary row drops its zero counts first on a narrow screen and ends
  in an ellipsis; stage rows show go/hold/untagged as small chips.
- At 16 colors a gray chip is black on light gray and keeps its caps.
- A short screen's splash shows the small Amos in color; the help shows
  keys in blue.

### Fixed

- The message after approving deployments says what dillodash does next,
  instead of stopping mid-sentence.

## [1.7.1]

### Fixed

- `D` in a pull request's detail finds the deployments waiting on a PR
  that someone else merged (a release job, a teammate) while the detail
  was open. The detail query now reads the merge commit, so a refresh no
  longer drops it. Before, `D` said `not merged` while `M` said `already
  merged, press D`. A merged PR whose answer still lacks the commit
  re-reads it instead of pointing at `M`, and an open detail falls back
  to its listed row's merge commit.

## [1.7.0]

A clock on the header row.

### Added

- The header shows the time in UTC and in a second zone:
  `2026-11-03 04:12:33.45 UTC  23:12:33.45 EST`. The second zone is New
  York by default, labelled `EST` or `EDT` by the season; `[display]
  clock_timezone` changes it (any IANA zone), `clock_tz_label` relabels it
  (`ET`), and `clock = false` hides the clock. Hundredths redraw at about
  30 frames a second; with `motion = "off"` the clock shows whole seconds
  and the dashboard wakes once a second instead.

### Changed

- The header keeps the acting login (`as alice`) ahead of the clock, the
  refresh time and the meters: a narrow terminal drops those first. Only
  the version outranks the login.

## [1.6.1]

Polish after 1.6.0: a release night's slots show from the first refresh,
the restart-first prompt 1.6.0 promised, and release rows that keep what
matters on a narrow screen.

### Fixed

- With releases on, the maintenance window reads the manifests from the
  first refresh (and after `R`), so tonight's slots and `w` no longer wait
  up to a refresh interval; a release that joins the queue mid-session
  pulls the next refresh in.
- `a`, `M`, `D` and `w` with a newer release installed ask `restart into
  vX first?` (1.6.0's notes promised it; the build asked only whether to
  go on): yes restarts in place, keeping the view and the open PR; no goes
  on with this build.
- Release rows size the number column by the `#N` they print, not
  `repo#N`, so their author and age stay; at 80 columns a release keeps
  its phase chip (slot, rail, `bumped · a: NOW`) and its branch title is
  clipped instead.
- `dillodash update` that can replace no install (a pinned extension,
  launchers only) says so and exits 1 instead of succeeding silently; its
  errors name the next step, and the notes excerpt stops at the first
  section instead of cutting a list item.
- The help legend fits its box and shows the bumped chip; the release
  help and the release card say `M` is the last resort, not the way a
  release merges at its slot.
- Overlays blank a cell either side, so the list's selection bar no
  longer shows against the help border.
- Countdowns under an hour read `starts in 4m`, not `0h04m`.

### Added

- `config check` prints `auto-update: on|off`, and what turned it off
  (`DILLODASH_AUTO_UPDATE` or `[update] auto`).
- `docs/keys.md` lists `a` on a release row and `y` outside the list.

### Changed

- `--demo` and `--demo-night` together are refused; `config init` on an
  existing file names the next step.
- No `env_followup` setting: `D` on a release row already approves
  whichever environment its deploy waits on, and the row names it.

## [1.6.0]

A release night in dillodash alone, and installs that keep themselves
current: release NOW from every place a release can be opened (#23), a
warning on a stale build (#22), the identity check, lane and slot cancel
of a manual night (#24), and a verified auto-update with `R` to restart
into it (#25).

### Fixed

- `a` on a release offers release NOW from every place a release can be
  opened, and never the core's "already approved by you for this head —
  a second review changes nothing" (#23). 1.5.3 let `a` reach the release
  workflow, but the workflow still let go of a release whose row did not
  carry its info: one opened with `:`, one whose state had not loaded, or
  one whose state a rate-limited refresh had dropped. Now:
  - With no row to go by, a PR open into `production_branch` of an
    enrolled repo from a `release_prefix` or `hotfix_prefix` head is a
    release. Before the enrolled repos are known, `a` says `couldn't read
    release state for <repo>#<n> yet — press r and retry` and posts
    nothing.
  - A release PR goes to the release workflow before the plan workflow,
    so a release whose thread quotes a plan digest is no longer answered
    as a plan.
  - A refresh that stops at a rate limit before the release workflow
    keeps the releases section instead of blanking it until the next
    clean refresh.
  - `M` on a release reads the same release state `a` does; before, it
    only looked at the row's own info.
- `:` reaches the enrolled release repos, not only the review repos.

- After release NOW on a release the window bumped or rolled, its row
  follows the command (dispatched, then the run) instead of staying
  `bumped` (#24).
- A message a workflow gives while a refresh is applied (a release
  landed, or FAILED) is no longer replaced by `refreshed at …` or cleared
  by the same refresh (#24).
- A merged release whose deploy waits on an environment names it:
  `merged; its deploy waits on stg — press D on its row` (#24).
- `M` on a release outside the window points at `a` while the release-now
  dispatch can fire: merging by hand is the last resort, for when the
  dispatch failed this session, the command has waited five minutes
  with no run (a restart forgets a failed dispatch), or
  `release_now_dispatch` is off (#24).

### Added

- A release the freeze bumped (the manifest item's `bumped` flag) ends
  its rail as `bumped` instead of being read as an unknown status. A
  bumped or rolled release you approved says `bumped — a: release NOW`
  on its chip, and its card's `a:` line says that NOW posts the command
  with no second review (#23).
- A stale build says so. At startup dillodash reads the latest release
  (a conditional request, cached in `~/.cache/dillodash`, quiet when
  offline); when it is newer than the running build, the header's
  version turns gold (`v1.5.2 → v1.5.4`) and the status line says
  `dillodash v1.5.2 — v1.5.4 is out: gh extension upgrade dillodash` on
  every screen (a standalone binary gets its own line). `a`, `M`, `D`
  and `w` first name what the build lacks — `release NOW on an approved
  release needs ≥ v1.5.3 — you run v1.5.2: …` — and ask before going on
  with it, instead of a generic answer that hides the missing fix (#22).
- `config check` reports the running version against the latest
  release, and lists every dillodash on the machine with its version:
  the gh extension, each `dillodash` or `gh-dillodash` on `PATH`, a
  launcher script as a launcher (never asked its version), and installs
  of different versions are named (#22).
- For workflow authors: `workflow.App.StaleFor(action)` and
  `workflow.StaleGate(app, action)` (#22).
- A manual release night in dillodash alone (#24):
  - **Identity before posting.** `[github] approvers_team = "org/team"`:
    before the review that queues a release, `/release-now`, `/attend`,
    `/release`, `/window-cancel` or `/approve`, dillodash checks the
    posting login is an active member and refuses otherwise, naming the
    login and, when the token came from `GH_TOKEN` or `GITHUB_TOKEN`, the
    variable. A bot login is refused with or without a team. When the
    token cannot read the team, it warns and posts. `config check` reports
    the membership. The token needs the organization's Members read for
    the check.
  - **`a` on a release row** in the list, without opening it first.
  - **The lane.** `[workflows.release_pr] lane_order`: when a release in
    the lane lands, its message names the next approved one (`next in
    lane: <repo>#<n> — a on its row: release NOW`); NOW on a release
    while another in the lane is still on its way asks first.
  - **A slot on the manifest.** release NOW on a release that holds a
    slot on a manifest offers `/window-cancel rel:<repo>#<n>` in the same
    flow (y/N).
  - **The countdown.** `[workflows.release_pr] apply_budget_mins`: a
    release in flight counts down on its row (`23m left`, `over by 4m`).
  - **`--demo-night`** replays the night offline, on its own clock (it
    starts at 22:55 ET on a window night whenever it is run): a bumped release out
    with release NOW, the next in the lane with its slot cancelled, its
    migration's `stg` and `prod` approved with `D`. The gallery has a shot
    of each step, and the release-pr doc a section on the night.
- dillodash keeps itself on the latest release (#25). A running
  dashboard checks at startup and every 30 minutes (conditional on the
  ETag); a newer release is downloaded in the background and installed
  only when its SHA-256 matches the release's `digest` **and** `gh
  attestation verify --repo solcyber/gh-dillodash` passes. It goes over
  every install dillodash may replace — the gh extension (its
  `manifest.yml` tag follows) and standalone binaries — by a rename
  beside each, the old binary kept as `<name>.prev`. A launcher script, a
  pinned extension and a path dillodash cannot write are left alone, the
  last with the one command that would update it. A refusal replaces
  nothing and stays on the status line, marked as an error. Never a
  downgrade, a draft or a prerelease.
- `R` restarts into the installed release on the same view: the open PR
  or issue, the selected row and the filter come back. It waits for an
  action in flight. Until then `a`, `M`, `D` and `w` offer the restart
  first (#25).
- `gh dillodash update` installs the latest release now and prints what
  changed; `gh dillodash update --rollback` swaps the `.prev` binaries
  back (#25).
- `[update]` with `auto` (default `true`; `false` keeps the check and
  the banner and installs nothing) and `channel` (`stable`), and
  `DILLODASH_AUTO_UPDATE=0` to pin one host whatever a pulled config
  says (#25).

## [1.5.4]

### Fixed

- `w` offers `/window-cancel rel:<repo>#<n>` on a release. A release
  queued by its approval never carries the queued label (the conductor
  labels it scheduled at the freeze), so the cancel entry was missing for
  every release, from its row under the night and from its detail view.
  Before the freeze the manifest does not list a release yet, and the
  conductor refuses a cancel for it, so the entry is marked `(not now)`
  and says a Request changes review takes it out instead (#20).
- A release merged with its deploy held at a required-reviewer
  environment no longer ends as *lost track* after 90 minutes. The
  outcome heading `## Release merged; the deploy is WAITING on an
  environment approval`, the label `window:pending` and the manifest
  status `PENDING …` end it as **`deploy waiting · D`** in gold, with one
  notice naming the environment and a card line pointing at `D`. When
  someone swaps `window:pending` for `window:applied` (or
  `window:failed`) by hand, the row follows the label (#20).
- Docs: `auth.md` names Deployments write, not Actions write, for `D`;
  `maintenance-window.md` covers release rows, the release cancel, and
  that the conductor, not dillodash, checks the team; `release-pr.md`
  lists the exact outcome headings dillodash reads (#20).

### Added

- `pending_label` under `[workflows.maintenance_window]` (default
  `window:pending`).

## [1.5.3]

### Fixed

- `a` on a PR you already approved reaches its workflow instead of
  stopping at "already approved by you for this head". The core refused
  first, so a release approved earlier and then bumped from the window
  could not be released NOW from dillodash, and a queued terraform plan
  could not be re-approved with `--outside-window`: both had to be posted
  by hand. Now:
  - `release_pr`: an approved release offers only **release NOW**, which
    posts `/release-now "<reason>"` and fires the dispatch with no second
    review; one already on its way posts nothing.
  - `terraform_approve`: the `/approve` (with `--outside-window` when
    asked) is posted and the approving review is skipped, since yours
    stands; with no live plan, `a` says the PR is approved and posts
    nothing.
  - A PR no workflow takes keeps the refusal.
- For workflow authors: `workflow.IApproved(app, pr)` is the core's rule
  (approved with `a` this session for the current head, or your review on
  GitHub). A workflow's `Approve` now runs before that check, so one that
  submits a review must look it up itself.

## [1.5.2]

### Changed

- The Amos who walks the rail in a release's mission-control card is the
  splash's Amos, small and in his own colors, where the terminal can show
  the sprite; the ASCII glyphs, `mascot = "ascii"` and 16 colors keep the
  line art.

## [1.5.1]

### Added

- `--demo` carries two Actions runs waiting on an environment, so the
  `waiting approvals` section, its summary and `D` on a run are on screen.
  The gallery gains the section, `D` on a run, and fortress mode over
  Chicago next to Manhattan.

### Fixed

- The gallery's scripts are checked against what they draw: each shot
  names the text its frame must show, and the README's picture must open
  the release. A new fixture row used to shift the cursor-driven shots
  silently onto the wrong row. The fortress shot no longer flips a coin
  for its city on every render.

## [1.5.0]

### Added

- **`waiting_runs`, a fourth optional workflow.** Actions runs that wait
  on an environment approval with no listed PR behind them (a production
  deploy dispatched by hand, an ops workflow run from `main`) get a
  `waiting approvals` section: the workflow, its `run-name` title, the
  branch, who started it, its age and a chip per environment
  (`approve: prod` when you are a reviewer, `waits: prod` when someone
  else is), and `N runs waiting, M on you (D)` in the summary. `D` on a
  row approves the environments you review, with `approval_comment`; when
  none is yours it names who can (or that you dispatched it and the
  environment forbids self-review). A run on a listed merged PR's merge
  commit (a release's deploy) stays under that PR; a run on an open PR's
  head is listed, since `D` on an open PR does not approve. New table
  `[workflows.waiting_runs]` with `enabled`, `repos` (default: the review
  repos) and `approval_comment`; the token needs Actions read and, for
  `D`, Deployments read and write on those repos.
  See [docs/workflows/waiting-runs.md](docs/workflows/waiting-runs.md).
- For workflow authors: `workflow.RowDeployer` lets a workflow's own row
  answer `D` before the merged-PR path; `ghapi.Client` gained
  `RepoWaitingRuns`; `model.WorkflowRun` carries `HeadBranch` and `Actor`,
  `model.PendingDeployment` its `Reviewers`.

### Fixed

- `release_pr` live progress:
  - A `/release-now` posted again after an outcome comment is followed
    again. Before, the old outcome won and the new run was never looked for.
  - Each flight's outcome is announced, so a second failure toasts like
    the first. A release that finished, or was lost, before the dashboard
    started is drawn but not announced on every launch.
  - A finished release's T+ clock and "released … after X" stop at the
    finish. A lost one stops at the timeout.
  - A failed dispatch to the manifest repo says on the card that nothing
    will pick the release up on its own.
  - A manifest "run cancelled" reads *cancelled*, not *failed*.
  - The poll clears the last cycle's error. The card says what happens
    next instead of always "retrying".
  - A hotfix's `a` menu no longer offers a queue it never joins.
- A poll that read the world before a refresh landed no longer overwrites
  the refresh's newer data. A poll that fails or panics says so on the
  warn line instead of freezing the rail in silence.
- An open release in flight no longer re-renders its whole description and
  comment thread every frame: the markdown is kept while only the card
  moves.
- The release row's kind column is measured with the widest clock, so the
  columns no longer slide as the clock grows. A narrower list keeps a
  phase-only chip before it drops the column. The card's phase names line
  up and fit *waiting for slot*.
- Eight screenshots in `docs/workflows/` pointed at `docs/docs/shots/`.
  The gallery test now resolves every image from its doc.

### Changed

- `config check` says where `/release-now` is dispatched and whether
  releases are followed live, and probes the manifest repo for write and
  for its apply workflow's runs.
- Config: `progress_poll_secs` has a floor of 10 and `apply_workflow`
  must be a file name. Warnings for an `apply_run_match` without
  `{number}`, and for dispatch or live-progress keys set while the
  maintenance window is off.
- Each screenshot's caption lives in one place (`ui.ShotCaptions`), and
  every doc's alt text must match it. The demo's pull requests have
  descriptions of their own, and the unicode shot uses the app's own
  glyph setup.

## [1.4.1]

### Fixed

- With `approval_queues_for_window = true`, an approved release now shows
  under its night in the window section, the way the conductor sees it:
  the approving review is the queue signal and nothing is labelled before
  the freeze, so a release waited for a `window:queued` label that never
  comes. The rule is the conductor's: an open, non-draft `release/*` PR
  with an APPROVED review no older than three nights; a hotfix never
  queues; a release on its way (released NOW, or at its slot) is back
  among the releases with its rail. The release query now reads each
  review's `submittedAt` for the age.

## [1.4.0]

### Added

- **Live release progress.** A release sent on its way (`/release-now`, or
  the night's manifest) is followed to its outcome: the manifest repo's
  apply run step by step, the PR's outcome comment and labels, the window
  item's status and the app repo's deploy runs. The row shows a **rail**
  that fills phase by phase with the clock (`deploying 3/9 · 2m`), the
  detail a mission-control card (T+ clock, the rail with Amos on it, one
  row per phase with its clock and technical tail, the outcome, the run),
  and one message says how it ended, with the launch when it landed.
  Polling runs only while something is in flight, at most a few small
  calls per release every `progress_poll_secs`; the whole-dashboard
  refresh is untouched. New keys under `[workflows.release_pr]`:
  `apply_workflow`, `apply_run_match`, `progress_poll_secs`,
  `progress_timeout_secs`; under `[workflows.maintenance_window]`:
  `applied_label`, `failed_label`. `--demo` carries a release in flight.
- For workflow authors: `workflow.Poller` (a light poll between
  refreshes), `RowKind.Live` (a row that keeps the frame clock running),
  `ManifestItemProvider` and `ScheduledProvider`; `ghapi.Client` gained
  `WorkflowFileRuns` and `RunJobs`. The deploy watch still rides the full
  refresh; moving it onto the poller is a follow-up.
- `config check` prints the token's role in every review repo (`write:
  yes (ADMIN)`, `write: NO (READ)`), read from the list query at no extra
  cost, and fails when every review repo is read-only, so a wrong `gh`
  account or a read-only collaborator is caught before the first `a` hits
  a 403. The closing line now says what was probed and what was not
  (fine-grained grants, Actions write, protection rules). (#8)

## [1.3.0]

### Added

- `release_pr`: *release NOW* also tells the maintenance-window repo.
  After the review and the `/release-now "<reason>"` comment land, the
  dashboard fires a `repository_dispatch` on the window's `manifest_repo`
  (`release_now_dispatch`, default true; event
  `release_now_dispatch_event`, default `release-now`; payload
  `{repo, pr, comment_id}`), because a comment in the release repo never
  reaches the manifest repo's workflows on its own. The payload only names
  the comment; the receiver re-reads it. A failed dispatch after the
  comment landed is reported on the status line with a pointer at `M`.
  The token needs Contents write on the manifest repo for it (#11).
- `ghapi.Client` gained `PostComment` (the comment's id) and `Dispatch`.

### Fixed

- The release workflow's notes step matched the CHANGELOG heading
  `[X.Y.Z]` against the tag with its brackets, so it found no notes. It
  now strips the brackets first.

## [1.2.0]

A polish release: three filed issues, several latent bugs, and a pass
over every surface of the app, the command line and the docs.

### Fixed

- `release_pr` rows showed no CI with a fine-grained PAT: the release
  workflow read only the GraphQL check rollup, which such a token cannot
  see. Release PRs now go through the same Actions fallback as the review
  list, in one CI pass with one cache (#5).
- `config check` passed, and the dashboard would post, as a login listed
  in `[bots]`. Both refuse now, naming the token source to fix; a token
  from `GH_TOKEN` or `GITHUB_TOKEN` gets a note that it overrides the `gh`
  login, and `docs/auth.md` says so (#6).
- The detail heading of a PR from a repo other than the sole review repo
  read a bare `#N`; it always carries the repo now. The list key bar
  shows `D deploys` while `release_pr` is on, and the help lists `D`
  under the list (#7).
- Ctrl-C inside `$PAGER` or `$EDITOR` killed the dashboard once the child
  exited. Bubble Tea owns the signals during the TUI now; Ctrl-C on the
  dashboard itself quits (exit 130), `q` during an action warns once and
  quits on a second press.
- The release workflow re-annotated the previous refresh's rows on the
  refresh goroutine while the UI could be reading them (a concurrent map
  write); the rows are cloned first.
- One failing chunk of repos threw the whole refresh away; a chunk's
  error now lands as a warning on its repos and the rest show. A rate
  limit met by a workflow backs the refresh off like one met by the list.
- The Actions CI fallback switched itself off for a repo on any error,
  a timeout included, until restart; it now latches only on a permission
  refusal, keeps the last cluster through a transient failure, and `r`
  re-tries a latched repo.
- Secondary (abuse) rate limits without `Retry-After` were retried after
  15 s; they wait at least a minute now, and an HTTP-date `Retry-After`
  is parsed. A PR with more than fifty checks could show green while a
  check past the page failed; the rollup state covers the rest.
- `ci_recheck_secs`, `deploy_stuck_secs`, `deploy_refresh_secs` and
  `list_other_prs` were parsed and did nothing (or only changed a
  message); all four work.
- Pasting into the comment, filter or go-to prompt did nothing, and an
  IME commit or an emoji with a modifier was dropped; both insert now.
- `config init --help`, `config migrate --help` and `font install --help`
  acted instead of printing help; every subcommand has a real flag set.
- `config pull` silently replaced a hand-written config or switched the
  repo it followed; both need `--force` now, and the previous file is
  kept as `config.toml.bak`. Config writes are atomic.
- On Windows without `HOME`, `config init` wrote `.config` relative to
  the current directory; `USERPROFILE`, `%APPDATA%` and the OS home are
  honoured. The browser opens through `rundll32` instead of `cmd /c
  start`, which interpreted `&` in a branch name; refs in URLs are
  escaped.
- `[bots]` logins matched by prefix (a configured `al` made `alice` a
  bot); the match is exact, with or without `[bot]`.
- A second `a` on a PR you already approved posted a second review; Esc
  on the inline comment prompt said "empty"; `n` with no search said `no
  match for ""`; raw markdown mode and the scroll glide leaked from one
  PR to the next; a search followed you into the diff; the cursor jumped
  to the top when its row was filtered away.
- Below about fifty columns PR titles vanished and the help overlay lost
  its legend and hint on short terminals; file paths lost their base
  name; ages past two years overflowed the column; a search highlight
  shifted on text whose lower-casing changes its length (`İ`).
- `go test ./...` opened the test fixture's `acme` URLs in the
  developer's browser and overwrote their clipboard: the UI tests press
  `o` and `y`, which started the real `open` and `pbcopy`. The model
  reaches the browser and the clipboard through hooks the test harness
  scripts, and a test checks they stay scripted.

### Added

- `docs/keys.md` is generated from the key table the dashboard runs on,
  and tests keep the key bar, the help overlay, the doc and the workflow
  package's reserved keys in step.
- `e` shows the last status or error message in full; a line wider than
  the terminal ends in a visible ellipsis. The help overlay scrolls.
- `gh dillodash font license` prints the bundled font's licences, and
  `font install` writes them beside the font files.
- `:` accepts `owner/repo#N`, and refs read `owner/repo#N` when the
  configured repos span owners (two repos with the same name under
  different owners no longer share state).
- `GH_HOST` is honoured when `[github] host` is unset; `GH_EDITOR` and
  `GH_PAGER` win over `VISUAL`/`EDITOR` and `PAGER`; Windows falls back
  to `notepad` and `more`; the clipboard prefers a real tool and reports
  OSC 52 honestly.
- The dashboard refuses to draw into a pipe and points at `config check`.
- Release builds carry artifact attestations and run the test matrix
  first; CI runs the race detector, staticcheck, govulncheck and `go mod
  tidy -diff`.

### Changed

- Built with Go 1.27.3 (stdlib fixes for `net/http`, `crypto/tls`,
  `crypto/x509`); building from source needs Go 1.27.
- Status text follows one convention (lowercase, em dashes, keycap
  names); GitHub review states read `changes requested`; short SHAs are
  seven characters everywhere.
- Idle, the dashboard sleeps until the next second a countdown shows or
  the next refresh, instead of ticking 2.5 times a second.
- The maintenance window's `cutoff < release_from < start < end` is an
  error, not a warning.
- Doctor errors print once; a bad token exits 2 like every other refusal
  to start.

## [1.1.0]

- `gh dillodash config pull owner/repo[:path]` installs the config from a
  repository through the `gh` token, validates it first, and records its
  source; a bare `config pull` refreshes it and `config check` reports
  whether the installed copy is current.

## [1.0.0]

First public release of dillodash, a rewrite in Go of an internal Python
dashboard, as a GitHub CLI extension.

- Multi-repo review list with status chips, CI glyphs (with the Actions
  fallback for tokens that cannot read the check rollup), diffstat, author,
  age; open issues below.
- Detail view with rendered markdown, the review timeline, changed files,
  in-app diffs and `$PAGER`; approve, comment, tag, merge, approve
  deployments.
- TOML configuration, `config check`, `config init`, `config migrate` from
  the old INI format; `gh` authentication, GitHub Enterprise Server.
- Optional workflows: `terraform_approve`, `maintenance_window`,
  `release_pr`, each with its protocol documented.
- Fortress mode, the splash, wipes, confetti, the release launch, and Amos.
- The bundled Hack Nerd Font Mono and `font install`.

[1.4.1]: https://github.com/solcyber/gh-dillodash/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/solcyber/gh-dillodash/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/solcyber/gh-dillodash/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/solcyber/gh-dillodash/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/solcyber/gh-dillodash/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/solcyber/gh-dillodash/releases/tag/v1.0.0
