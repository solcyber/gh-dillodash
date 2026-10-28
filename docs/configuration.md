# Configuration

dillodash reads one TOML file. It looks, in order, at `--config PATH`,
`$DILLODASH_CONFIG`, `$XDG_CONFIG_HOME/dillodash/config.toml`, and
`~/.config/dillodash/config.toml`. `gh dillodash config init` writes the
annotated example there; `gh dillodash config check` validates a config and
probes GitHub with it.

[`config.example.toml`](../config.example.toml) shows every key with its
default and a comment. This page explains the sections.

## `[github]`

| Key | Default | Meaning |
|---|---|---|
| `host` | `github.com` | A GitHub Enterprise Server hostname. Sign in to it with `gh auth login --hostname HOST`; dillodash uses that token. |
| `login` | unset | The login you expect to act as. When `gh` is signed in as someone else dillodash warns, pauses three seconds, and continues as that account (`gh auth switch` changes it). |

There is no token in the config. dillodash asks the `gh` CLI for the token
of `host`, the way every `gh` extension does.

## `[reviews]` (required)

| Key | Default | Meaning |
|---|---|---|
| `repos` | — | The repositories whose open pull requests and issues are listed, as `"owner/name"`. One or many; several owners are fine. `--repo OWNER/NAME` (repeatable) replaces the list for one run. With more than one repo every reference reads `repo#N`. |
| `ci_recheck_secs` | `900` | When CI is read through the Actions API (a fine-grained token cannot read the check rollup), a settled head commit is re-checked after this long; a failing one on every refresh. |

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
| `logins` | `["github-actions", "dependabot"]` | Authors drawn as bots (dimmed, blue) in the list and the timeline. A login ending in `[bot]` always counts. Add your own automation account. |

## `[workflows.*]`

Each workflow is off unless its table exists with `enabled = true`. A table
without `enabled` warns and stays off, so the example config can show every
key without turning anything on. Every knob a workflow has is in the
example; the docs under [workflows/](workflows/) explain the protocols:

- [`terraform_approve`](workflows/terraform-approve.md)
- [`maintenance_window`](workflows/maintenance-window.md)
- [`release_pr`](workflows/release-pr.md)

Cross-checks the loader enforces: `release_pr.approval_queues_for_window`
needs `maintenance_window` enabled; `release_pr.org` is required when the
review repos span several owners; `maintenance_window.manifest_repo`
defaults to the first review repo (with a warning when there are several);
times are `HH:MM`, zones are IANA names, and the window's
`cutoff < release_from < start < end` on one day.

## Precedence and errors

A flag beats the config, which beats the default. Unknown keys and tables
warn with a "did you mean" and are ignored; wrong types, bad choices, bad
times and malformed repo names are errors. Errors read
`<path>: [section] key: why`.

## Migrating from pr-dash

`gh dillodash config migrate` converts a `~/.config/pr-dash/config.ini`
(pass `--from PATH` for another location). Token sources are dropped (the
`gh` login replaces them), `[defaults]` becomes `[reviews]` and
`[display]`, and the three workflows are written enabled with the defaults
the old build hard-wired, headed by a comment telling you to disable the
ones your repos do not speak.

## Files dillodash writes

| Path | What |
|---|---|
| `~/.cache/dillodash/release-repos-<org>.json` | The `release_pr` workflow's discovered repo list, cached for `discover_ttl_secs`. Written 0600. Delete it to re-discover now. |
| `$TMPDIR/dillodash-*.md`, `*.diff` | A comment being edited in `$EDITOR`, a diff shown in `$PAGER`. Created 0600 and removed afterwards. |

Nothing else. The token never touches the disk through dillodash.
