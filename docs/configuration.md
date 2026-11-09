# Configuration

dillodash reads one TOML file. It looks, in order, at `--config PATH`,
`$DILLODASH_CONFIG`, `$XDG_CONFIG_HOME/dillodash/config.toml`
(`%APPDATA%\dillodash\config.toml` on Windows), and
`~/.config/dillodash/config.toml`. `gh dillodash config init` writes the
annotated example to the first of those that is set (`--config`,
`$DILLODASH_CONFIG`, else the default); `gh dillodash config check`
validates a config and probes GitHub with it. Every write (`init`,
`migrate`, `pull`) is atomic, and an overwrite keeps the previous file as
`config.toml.bak`.

[`config.example.toml`](../config.example.toml) shows every key with its
default and a comment. This page explains the sections.

## `[github]`

| Key | Default | Meaning |
|---|---|---|
| `host` | unset | A GitHub Enterprise Server hostname. Unset, the host is `$GH_HOST`, then `gh`'s default host, then `github.com`. Sign in to it with `gh auth login --hostname HOST`; dillodash uses that token. |
| `login` | unset | The login you expect to act as. When `gh` is signed in as someone else dillodash warns, pauses three seconds, and continues as that account (`gh auth switch` changes it). |
| `approvers_team` | unset | `"org/team"`. Before posting a command a bot accepts only from an approver (the review that queues a release, `/release-now`, `/attend`, `/release`, `/window-cancel`, `/approve`), dillodash checks the login is an active member, and refuses otherwise. Reading the team needs the organization's Members read on a fine-grained token; without it dillodash warns and posts. |
| `approvers_team_strict` | `false` | `true` refuses that command when the team cannot be read (a fine-grained token without the organization's Members read, or a failed check) instead of warning and posting unchecked. Use it on a dashboard whose approvals must come from the team: grant the token Members read. |
| `allowed_repos` | unset | `["owner/name", ...]`: the only repos dillodash writes to. Every comment, review, merge, command, label, assignment, close or reopen, deployment approval or rejection, re-run, cancel and dispatch to any other repo is refused before a prompt, and again in the GitHub client underneath, so no key can go around it, including an item opened with `:`. Reading is unaffected. A workflow or `[[workflows.dispatch]]` set to write to a repo outside the list is a warning. Unset, every repo may be written to. |

There is no token in the config. dillodash asks the `gh` CLI for the token
of `host`, the way every `gh` extension does; `GH_TOKEN` and
`GITHUB_TOKEN` in the environment override the `gh` login, as they do for
`gh` itself (see [auth.md](auth.md)).

## `[reviews]` (required)

| Key | Default | Meaning |
|---|---|---|
| `repos` | — | The repositories whose open pull requests and issues are listed, as `"owner/name"`. One or many; several owners are fine. `--repo OWNER/NAME` (repeatable) replaces the list for one run. With more than one repo every reference reads `repo#N`. |
| `requested_repos` | `[]` | Busy repositories listed only for the open pull requests that ask for your review (directly or through a team), that you reviewed, or that you opened: no issues and none of their other PRs. A PR you approved stays listed after the request is gone, so an approval that goes stale comes back as `re-review`. Read through GitHub search (three searches per request); a repo may not also be in `repos`, and `--repo` drops the list for that run. |
| `merge_applies` | `[]` | Repositories where a merge deploys or applies (a pipeline runs on push to the base). `M` there opens its question with "MERGING APPLIES", and the PR's header carries a "merge applies" chip. |
| `ci_recheck_secs` | `900` | When CI is read through the Actions API (a fine-grained token cannot read the check rollup), a settled, passing head commit is re-checked after this long; a failing or running one on every refresh; everything on `r`. |

## `[update]`

| Key | Default | Meaning |
|---|---|---|
| `auto` | `true` | Keep this install on the latest release. A running dashboard checks at startup and every 30 minutes (a conditional request: free when nothing changed); a newer release is downloaded in the background, verified (its digest and its build attestation, [SECURITY.md](../SECURITY.md)), and installed over every gh extension or standalone binary it may replace, the old one kept as `<name>.prev`. The status line then says `dillodash vX.Y.Z installed — R: restart now (keeps your place)`, and `a`, `M`, `D` and `w` offer that restart first. `false` keeps the check and the banner and installs nothing (a pinned host). `DILLODASH_AUTO_UPDATE=0` turns it off on one host whatever a pulled config says. |
| `channel` | `stable` | Which releases count: `stable`, the published releases. A draft or a prerelease is never installed. |

`gh dillodash update` installs the latest release now (whatever `auto`
says) and prints the first lines of what changed; `gh dillodash update
--rollback` puts the `.prev` binaries back. A launcher script on `PATH`
(a wrapper that sets a token and runs `gh dillodash`) is never replaced:
it runs whatever it wraps.

## `[display]`

| Key | Default | Flag | Meaning |
|---|---|---|---|
| `refresh_secs` | `300` | `--refresh` | Background refresh interval, floor 5. Lazy on purpose: each refresh spends GraphQL points from the token's hourly budget, and `r` refreshes instantly whenever you want fresh data. |
| `icons` | `nerd` | `--icons` | `nerd` needs a Nerd Font in the terminal (`gh dillodash font install`); `unicode` uses plain symbols; `ascii` uses letters. `gh dillodash glyph-test` shows what the terminal can draw. |
| `theme` | `auto` | `--theme`, `--mono` | `mono` uses no colors, only bold, underline and reverse. `NO_COLOR` in the environment does the same. |
| `motion` | `full` | `--motion` | `off` is the static dashboard: no splash, wipes, glints, confetti or launch. |
| `mascot` | `sprite` | | How Amos is drawn: `sprite` (half-block pixel art in his own colors), `ascii` (line art), `none`. |
| `clock` | `true` | | The header clock: the UTC date and time, then the same instant in `clock_timezone`, `2026-11-03 04:12:33.45 UTC  23:12:33.45 EST`. Hundredths with `motion = "full"`, whole seconds with `off`. `false` hides it. |
| `clock_timezone` | `America/New_York` | | IANA zone of the clock's second time, e.g. `Europe/London`, `Asia/Tokyo`, `UTC`. |
| `clock_tz_label` | `""` | | Label for the second time, e.g. `ET`. Empty uses the zone's own abbreviation for the instant, so New York reads `EST` in winter and `EDT` in summer. |
| `room` | `true` | | While the release window is open, the dashboard is the view out of a window: it shrinks to the glass in the middle of the screen, behind a cross of mullions, and the room around it has curtains, plants, pictures, a wall clock and a mug of something hot. `W` steps out to the plain dashboard (and back in); the next window opens in the room again. Needs a terminal of at least 100×30. `false` keeps the plain dashboard, and `W` still shows the room. |
| `profile` | unset | | A short badge in the header (at most 12 characters), e.g. `OPS`: which config this dashboard runs, for someone who runs more than one (`--config` or `$DILLODASH_CONFIG` switches between them). |

## `[agent]`

| Key | Default | Meaning |
|---|---|---|
| `handoff_label` | unset | The label `H` adds to hand the open PR or issue to an agent (after a confirm). An agent that is a GitHub App's bot cannot be @-mentioned into a notification, so it watches a label instead. Unset, `H` is off and not in the bar. |
| `handoff_comment` | unset | Posted with the label when set: the agent's first instruction, or a note for the humans watching the item. |
| `name` | unset | What the dashboard calls the agent: `H` asks "hand #6 to <name>?", the bar says "to <name>", and its PRs and comments show the name. Unset: "the agent". |
| `login` | unset | The agent's GitHub login (an App's bot login, with or without `[bot]`): its rows, comments and reviews are drawn as the agent's, by name, not as a bot's. |
| `section` | `false` | A list section named for the agent: its own open PRs and the open items carrying `handoff_label`, moved out of the PR and issue sections (its PRs still count toward "need you"). See [Working with an AI agent](agents.md). |

## `[bots]`

| Key | Default | Meaning |
|---|---|---|
| `logins` | `["github-actions", "dependabot"]` | Authors drawn as bots (dimmed, blue) in the list and the timeline. The match is exact (case-insensitive), with or without a `[bot]` suffix, and a login ending in `[bot]` always counts. Add your own automation account. The signed-in login may not be one of these: dillodash refuses to post as a bot, and `config check` fails. |

## `[merge]`

`methods` names the merge method `M` uses per repo, `{ "acme/infra" =
"squash" }`: a repo whose branch rules want a linear history refuses a
merge commit. A repo not listed merges with `[workflows.release_pr]
merge_method`, else as a merge commit. Each value is `merge`, `squash` or
`rebase`.

## `[[workflows.dispatch]]`

The workflows `X` may run, one table per entry; nothing else is ever
dispatched, and `X` stays out of the bar while none is listed.

```toml
[[workflows.dispatch]]
name = "nightly window: dry-run freeze"   # the menu label, unique
repo = "acme/infra"
workflow = "window.yml"                   # workflow_dispatch on this file
ref = "main"                              # the ref it runs on (default "main")
confirm = "a dry run posts nothing"       # an extra line in the confirm
inputs = [
  { name = "phase", type = "choice", options = ["freeze", "run", "all"], default = "freeze" },
  { name = "dry_run", type = "boolean", default = true },
  { name = "night", description = "YYYY-MM-DD; empty = tonight" },
]

[[workflows.dispatch]]
name = "close a night by hand"
repo = "acme/infra"
event = "window-close"                    # a repository_dispatch event instead
inputs = [{ name = "night", required = true }]
```

Set exactly one of `workflow` and `event`. An input's `type` is `string`
(the default), `boolean`, `choice` (with `options`) or `number`; a
`default` must fit its type, and `required` refuses an empty answer. `X`
asks the inputs in order (a choice or a boolean as a menu with the default
first, so enter takes it), shows the exact request (`POST
repos/acme/infra/actions/workflows/window.yml/dispatches {...}`) with the
`confirm` line, and sends it as you. An empty optional input is left out,
so the workflow's own default applies. `workflow_dispatch` inputs travel
as strings; an event's `client_payload` keeps booleans and numbers typed.
After a `workflow_dispatch` the status line finds the new run (yours, of
that file, since the dispatch) and follows it to its conclusion. The token
needs Actions write for a workflow and Contents write for an event.

## `[workflows.*]`

Each workflow is off unless its table exists with `enabled = true`. A table
without `enabled` warns and stays off, so the example config can show every
key without turning anything on. Every knob a workflow has is in the
example; the docs under [workflows/](workflows/) explain the protocols:

- [`terraform_approve`](workflows/terraform-approve.md)
- [`maintenance_window`](workflows/maintenance-window.md)
- [`release_pr`](workflows/release-pr.md)
- [`waiting_runs`](workflows/waiting-runs.md)
- [`promote`](workflows/promote.md)

Cross-checks the loader enforces as errors: `release_pr.apply_run_match`
accepts only the `{repo}` and `{number}` placeholders, and
`waiting_runs.approval_comment` and `promote.approval_comment` only `{repo}`, `{workflow}` and `{run}`;
`promote.tiers` is three distinct branches and `promote.first_tier_reviews` is `requested` or `open`;
`release_pr.apply_workflow` is a file name (`apply.yml`), not a path;
`release_pr.approval_queues_for_window`
needs `maintenance_window` enabled; `release_pr.org` is required when the
review repos span several owners; times are `HH:MM`, zones are IANA
names, and the window's `cutoff < release_from < start < end` on one day;
`maintenance_window.reserved_from` and `reserved_to` come together; a
`[[workflows.dispatch]]` entry sets exactly one of `workflow` and `event`,
has a unique `name`, and its inputs' defaults fit their types; `[merge]
methods` names `owner/name` repos with `merge`, `squash` or `rebase`.

And as warnings: `maintenance_window.manifest_repo` defaults to the first
review repo when there are several; `release_pr.progress_poll_secs` has a
floor of 10 (0 turns it off), and so does `waiting_runs.poll_secs`; an `apply_run_match` without `{number}`
would match every release of a repo; `release_now_dispatch`,
`apply_workflow` and `progress_poll_secs` do nothing without
`maintenance_window`, whose `manifest_repo` runs the release.

## Installing the config from a repo

A pulled config stays current by itself in the dashboard too: about once
an hour it compares the installed file's blob sha with its source's. When
the repo has a newer file, the status line says `config outdated: <repo>
has a newer <path> (P pulls it and restarts)`, and `P` in the list pulls
it (refusing one that does not validate, as `config pull` does) and
restarts into it, keeping your place. A config somebody edited on the
dashboard host is never compared or replaced.

An organization can keep its config in a repository its members can read
(private is fine: the fetch goes through the `gh` token) and install it
with one command:

```sh
gh dillodash config pull acme/dash-config            # config.toml at the repo root
gh dillodash config pull acme/dash-config:team/x.toml
gh dillodash config pull                             # refresh from the recorded source
```

The pulled file is validated with the same parser the dashboard uses and
refused if it does not pass, so a broken commit never replaces a working
config. The installed copy starts with two comment lines recording the
source, the blob sha and the time; a bare `config pull` reads them, and
`config check` says `source: current` or `OUTDATED` after asking GitHub
for the file's current sha.

`config pull` never silently replaces a config you wrote by hand, or
switches the repo an installed one follows: both need `--force`, and the
previous file is kept as `config.toml.bak`. `--force` also reinstalls an
unchanged file. `--config PATH` installs elsewhere (otherwise
`$DILLODASH_CONFIG`, then the default path, so the dashboard reads what
was just written); `--host HOST` reads from a GitHub Enterprise Server.
Personal settings (`--icons`, `--motion`, `--refresh`) stay on the command
line, so the pulled file is the whole config.

## Precedence and errors

A flag beats the config, which beats the default. Unknown keys and tables
warn with a "did you mean" and are ignored; wrong types, bad choices, bad
times and malformed repo names are errors. Errors read
`<path>: [section] key: why`.

## Migrating a legacy INI config

`gh dillodash config migrate` converts the predecessor tool's
`config.ini` from its default place under `~/.config` (pass `--from PATH`
for another location). Token sources are dropped (the
`gh` login replaces them), `[defaults]` becomes `[reviews]` and
`[display]`, and the legacy tool's three workflows (`terraform_approve`,
`maintenance_window`, `release_pr`) are written enabled with their
defaults, headed by a comment telling you to disable the ones your repos
do not speak. `waiting_runs` is new in dillodash; add its table by hand.

## Files dillodash writes

| Path | What |
|---|---|
| `$XDG_CACHE_HOME/dillodash/release-repos-<org>.json` (default `~/.cache/dillodash/`) | The `release_pr` workflow's discovered repo list, cached for `discover_ttl_secs`. Written 0600. Delete it to re-discover now. |
| `$XDG_CACHE_HOME/dillodash/latest-release.json` (default `~/.cache/dillodash/`) | The latest dillodash release and its ETag, from the build check at startup and in `config check`. Each check is a conditional request (free when nothing changed); offline, the last answer stands. Written 0600. |
| `<install>.prev`, and `manifest.yml.prev` beside the gh extension | The binary (and gh's manifest) an update or `gh dillodash update` replaced. `gh dillodash update --rollback` swaps it back. A temporary `.dillodash-*` file beside an install exists only while an update is written. |
| `config.toml.bak` beside the config | The previous config, kept whenever `config pull --force` or `config migrate --force` replaces one. |
| `$TMPDIR/dillodash-*.md`, `*.diff` | A comment being edited in `$EDITOR`, a diff shown in `$PAGER`. Created 0600 and removed afterwards. |

Nothing else. The token never touches the disk through dillodash.
An update rewrites its own binaries (above), never anything of `gh`'s
but the dillodash extension's `manifest.yml` tag.
