# Changelog

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

[1.2.0]: https://github.com/solcyber/gh-dillodash/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/solcyber/gh-dillodash/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/solcyber/gh-dillodash/releases/tag/v1.0.0
