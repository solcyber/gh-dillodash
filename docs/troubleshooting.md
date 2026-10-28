# Troubleshooting

Start with `gh dillodash config check`: it validates the config, shows
which account `gh` is signed in as, and probes what the token can read in
every repo.

| Symptom | Cause / fix |
|---|---|
| `GitHub auth failed` at startup | `gh` is not signed in to the host, or its token expired. `gh auth status`, then `gh auth login` (`--hostname` for GHES). |
| `warning: config expects login "x" but gh is signed in as "y"` | `gh auth switch`, or change `[github] login`. dillodash continues as `y` after a 3-second pause; ctrl-C if that is wrong. |
| Status line: `GitHub rate limit hit — refresh paused Ns` | The token hit its hourly budget, usually a `refresh_secs` set too tight for a busy day. dillodash pauses and resumes by itself; `r` waits too. Raise the interval if it recurs. |
| Status line: `degraded: CI via Actions API failed … needs Actions: read` | The token lacks the Actions permission; the CI column shows `?`. The fallback disables itself for that repo for the session; restart after fixing the token. |
| CI column empty for a PR | No workflow runs exist for that head (a docs-only change). Distinct from `?`, which means not visible. |
| Warn line: `issues hidden — token needs Issues: read` | The token cannot read issues, so the section is empty. Add the permission. |
| Status line: `repository not visible to this token` | A repo in `[reviews]` is mistyped, or a fine-grained token is still pending org approval or lacks the grant. |
| List says `couldn't load … retrying in Ns` | The refresh failed (network, 5xx). It retries in 15 s, 30 s, 60 s … up to the refresh interval; `r` retries now. |
| Empty boxes or `?` where icons should be | The terminal's font is not a Nerd Font. `gh dillodash font install`, select "Hack Nerd Font Mono" in the terminal, or `icons = "unicode"`. `gh dillodash glyph-test` shows every glyph. |
| Icons overlap the next column | The font is a Nerd Font but not a **Mono** variant: its icons are wider than one cell. |
| Background looks dark gray, or colors look off | The terminal does not advertise its colors. Use a `TERM` of `xterm-256color` or `tmux-256color`, or set `COLORTERM=truecolor` where the terminal supports it. |
| `y` says copied, but the clipboard is empty | The terminal ignores OSC 52, or an old tmux lacks `load-buffer -w`. The URL is on the status line to copy by hand. |
| `M` or `D` answers `403 … not accessible by personal access token` | The token needs Contents (merge) or Deployments (approvals) read and write on that repo. The message names it. |
| A merged PR says `stuck? …` | A run on its merge commit has been queued or running for over 45 minutes. Check it in that repo's Actions; dillodash only reads runs, never cancels or re-runs them. |
| The `releases` section is missing a repo enrolled today | Discovery is cached for a day. Delete `~/.cache/dillodash/release-repos-<org>.json` and press `r`. |
| Too much motion, or a slow SSH link | `--motion off` (or `motion = "off"`). Ambient effects rest by themselves after five minutes without a key. |
| Detail header says `last 100 of N comments` | GraphQL returns at most 100 comments, the newest. Older ones (and digests in them) are not seen; read the thread on GitHub. |
| A workflow's command "bounced" | dillodash posts the comment; the bot still validates it and replies on the PR or issue. The reply is shown on the next refresh. |
