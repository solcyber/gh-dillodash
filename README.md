# dillodash

A terminal dashboard for the pull requests that are waiting on you. Open one,
read it, approve it, comment, merge it, approve its deployments, and watch CI
without leaving the terminal. Runs as a GitHub CLI extension, so it signs in
the way `gh` does, and it works against any GitHub organization, including
GitHub Enterprise Server.

![dillodash](docs/screenshot.png)

Amos the armadillo lives in the splash screen and keeps you company when the
list is empty.

## Install

```sh
gh extension install solcyber/gh-dillodash
gh dillodash font install      # optional: the bundled Nerd Font for the icons
```

Sign in first with `gh auth login` if you have not. Then:

```sh
gh dillodash config init       # writes ~/.config/dillodash/config.toml
$EDITOR ~/.config/dillodash/config.toml
gh dillodash config check      # validates it and probes what your token can read
gh dillodash
```

The smallest working config is three lines:

```toml
[reviews]
repos = ["acme/infra", "acme/web-portal"]
```

Everything else is optional and documented in [`config.example.toml`](config.example.toml).

## What you see

- **The list.** Open pull requests across your repos, with a status chip
  (`awaiting review`, `approved by me`, `changes requested`, `DRAFT`), one
  glyph per CI check, the diffstat, the author and the age. Open issues sit
  below in their own section. `/` filters live as you type, `A` shows PRs
  approved by others, `:` opens any `#N` or `repo#N`.
- **A pull request.** The description and the comment thread rendered as
  markdown (tables, code, terraform plans colored), the review timeline, the
  changed files (`f`) and each file's diff, or the whole diff in `$PAGER`
  (`v`). `a` approves, `c`/`C` comment, `t` tags someone, `M` merges an
  approved PR, `D` approves the deployments waiting on a merged one.
- **The summary row** counts what needs you and what is failing; the status
  line explains every refusal in words; the header shows the GraphQL budget
  and when the next refresh lands.
- **Fortress mode** (`d`): everything open as a Dwarf Fortress map over
  Manhattan or Chicago. Row is age, lane is repo, shape is what it is.

It refreshes in the background every five minutes by default (`r` refreshes
now) and backs off by itself when GitHub rate-limits the token.

## Workflows

If your repositories run a bot that speaks one of these protocols, turn the
matching workflow on in the config and dillodash drives it:

| Workflow | What it adds | Doc |
|---|---|---|
| `terraform_approve` | `a` posts `/approve <plan-id>` from the plan digests on the PR, with the destroy count and the maintenance-window choice; plan cards in the detail view; apply outcomes read back | [docs/workflows/terraform-approve.md](docs/workflows/terraform-approve.md) |
| `maintenance_window` | a section of PRs queued for a nightly window, grouped by night with the countdown; `w` posts `/attend`, `/release` and `/window-cancel` on the night's manifest issue | [docs/workflows/maintenance-window.md](docs/workflows/maintenance-window.md) |
| `release_pr` | application releases as PRs into production across many repos, with the staging branch's units by tag, approve-to-queue or `/release-now`, `M` at the slot and `D` for the environment approvals | [docs/workflows/release-pr.md](docs/workflows/release-pr.md) |

Each doc specifies the comment formats the bot must emit, so any CI can be
taught to speak them. With no workflow configured dillodash is a plain,
fast reviewer.

## Keys

| Key | Where | Does |
|---|---|---|
| `j`/`k`, `g`/`G`, pgup/pgdn | list, detail | move, scroll |
| `enter` | list | open |
| `/` | list | filter (live); detail, diff: search (`n`/`N` next/prev) |
| `A` | list | toggle PRs approved by others |
| `a` | detail | approve |
| `c` / `C` | detail | comment inline / in `$EDITOR` |
| `t` | detail | tag someone (`$EDITOR` seeded with the mention) |
| `M` / `D` | detail (and `D` in the list) | merge an approved PR / approve its waiting deployments |
| `f`, `v` | detail | changed files, full diff in `$PAGER` |
| `m` | detail | raw markdown |
| `d` | list | fortress mode |
| `y`, `o` | anywhere | copy the URL, open it in the browser |
| `:` | anywhere | go to `#N` |
| `r` | anywhere | refresh now |
| `?` | anywhere | help, with the glyph legend |
| `q` | list | quit (`Esc` clears the filter instead) |

The full table is in [docs/keys.md](docs/keys.md).

## More

- [Configuration](docs/configuration.md): every key, with defaults.
- [Authentication and token permissions](docs/auth.md): what the `gh`
  token needs on each repo, and GitHub Enterprise.
- [Display](docs/display.md): the list columns, the chips, the colorblind
  palette, motion, and `--icons`/`--mono`/`--motion`.
- [Fonts](docs/fonts.md): the bundled Nerd Font and `gh dillodash font`.
- [Fortress mode](docs/fortress.md).
- [Troubleshooting](docs/troubleshooting.md).
- [Contributing](CONTRIBUTING.md), [Changelog](CHANGELOG.md), [Security](SECURITY.md).

dillodash is MIT licensed. The bundled Hack Nerd Font Mono is under the MIT
and SIL Open Font licenses (see `internal/fonts`).
