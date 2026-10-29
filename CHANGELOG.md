# Changelog

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
