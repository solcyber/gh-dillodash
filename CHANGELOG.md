# Changelog

## 1.1.0

- `gh dillodash config pull owner/repo[:path]` installs the config from a
  repository through the `gh` token, validates it first, and records its
  source; a bare `config pull` refreshes it and `config check` reports
  whether the installed copy is current.

## 1.0.0

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
