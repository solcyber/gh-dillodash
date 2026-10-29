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

There is no token in the config. dillodash asks the `gh` CLI for the token
of `host`, the way every `gh` extension does; `GH_TOKEN` and
`GITHUB_TOKEN` in the environment override the `gh` login, as they do for
`gh` itself (see [auth.md](auth.md)).

## `[reviews]` (required)

| Key | Default | Meaning |
|---|---|---|
| `repos` | — | The repositories whose open pull requests and issues are listed, as `"owner/name"`. One or many; several owners are fine. `--repo OWNER/NAME` (repeatable) replaces the list for one run. With more than one repo every reference reads `repo#N`. |
| `ci_recheck_secs` | `900` | When CI is read through the Actions API (a fine-grained token cannot read the check rollup), a settled, passing head commit is re-checked after this long; a failing or running one on every refresh; everything on `r`. |

## `[display]`

| Key | Default | Flag | Meaning |
|---|---|---|---|
| `refresh_secs` | `300` | `--refresh` | Background refresh interval, floor 5. Lazy on purpose: each refresh spends GraphQL points from the token's hourly budget, and `r` refreshes instantly whenever you want fresh data. |
| `icons` | `nerd` | `--icons` | `nerd` needs a Nerd Font in the terminal (`gh dillodash font install`); `unicode` uses plain symbols; `ascii` uses letters. `gh dillodash glyph-test` shows what the terminal can draw. |
| `theme` | `auto` | `--theme`, `--mono` | `mono` uses no colors, only bold, underline and reverse. `NO_COLOR` in the environment does the same. |
| `motion` | `full` | `--motion` | `off` is the static dashboard: no splash, wipes, glints, confetti or launch. |
| `mascot` | `sprite` | | How Amos is drawn: `sprite` (half-block pixel art in his own colors), `ascii` (line art), `none`. |

## `[bots]`

| Key | Default | Meaning |
|---|---|---|
| `logins` | `["github-actions", "dependabot"]` | Authors drawn as bots (dimmed, blue) in the list and the timeline. The match is exact (case-insensitive), with or without a `[bot]` suffix, and a login ending in `[bot]` always counts. Add your own automation account. The signed-in login may not be one of these: dillodash refuses to post as a bot, and `config check` fails. |

## `[workflows.*]`

Each workflow is off unless its table exists with `enabled = true`. A table
without `enabled` warns and stays off, so the example config can show every
key without turning anything on. Every knob a workflow has is in the
example; the docs under [workflows/](workflows/) explain the protocols:

- [`terraform_approve`](workflows/terraform-approve.md)
- [`maintenance_window`](workflows/maintenance-window.md)
- [`release_pr`](workflows/release-pr.md)
- [`waiting_runs`](workflows/waiting-runs.md)

Cross-checks the loader enforces as errors: `release_pr.apply_run_match`
accepts only the `{repo}` and `{number}` placeholders, and
`waiting_runs.approval_comment` only `{repo}`, `{workflow}` and `{run}`; `release_pr.approval_queues_for_window`
needs `maintenance_window` enabled; `release_pr.org` is required when the
review repos span several owners; times are `HH:MM`, zones are IANA
names, and the window's `cutoff < release_from < start < end` on one day.
`maintenance_window.manifest_repo` defaults to the first review repo, with
a warning when there are several.

## Installing the config from a repo

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

## Migrating from pr-dash

`gh dillodash config migrate` converts a `~/.config/pr-dash/config.ini`
(pass `--from PATH` for another location). Token sources are dropped (the
`gh` login replaces them), `[defaults]` becomes `[reviews]` and
`[display]`, and the three workflows are written enabled with their
defaults, headed by a comment telling you to disable the ones your repos
do not speak.

## Files dillodash writes

| Path | What |
|---|---|
| `$XDG_CACHE_HOME/dillodash/release-repos-<org>.json` (default `~/.cache/dillodash/`) | The `release_pr` workflow's discovered repo list, cached for `discover_ttl_secs`. Written 0600. Delete it to re-discover now. |
| `config.toml.bak` beside the config | The previous config, kept whenever `config pull --force` or `config migrate --force` replaces one. |
| `$TMPDIR/dillodash-*.md`, `*.diff` | A comment being edited in `$EDITOR`, a diff shown in `$PAGER`. Created 0600 and removed afterwards. |

Nothing else. The token never touches the disk through dillodash.
