# Keys

| View | Key | Action |
|---|---|---|
| list | `j`/`k`, arrows, `g`/`G`, pgup/pgdn/space | move |
| list | `enter`/`l` | open the PR or issue |
| list | `/` | filter, live as you type: words match the number, title, author, labels and a workflow's own text (a stack, a repo). Esc clears it |
| list | `A` | toggle PRs approved by others (issues are always all shown) |
| list | `d` | fortress mode; `d`/`q`/esc come back |
| list, detail | `D` | approve the deployments waiting on the selected (or open) merged PR |
| list, detail | `w` | with `maintenance_window`: `/attend`, `/release` or `/window-cancel` on the night's manifest issue, or open it |
| list | `q` | quit |
| any | `:` | go to `#N` or `repo#N`: a PR first, then an issue |
| any | `?` | help: every key, the glyph legend, the version |
| any | `y` | copy the URL (tmux buffer, OSC 52, or the system clipboard); the status line shows it either way |
| any | `o` | open the URL in a browser when this host has one; otherwise like `y` |
| any | `r` | refresh now (also re-checks CI that already finished) |
| detail | `a` | approve: a workflow may take over (`/approve` with a plan id, a release's queue-or-now choice); otherwise a GitHub review approval after a confirm |
| detail | `M` | merge the open, approved PR, naming its head so a branch that moved is refused by GitHub. A workflow may refuse or ask its own question first |
| detail | `c` / `C` | comment inline / in `$EDITOR` |
| detail | `t` | tag someone: pick a repo user, `$EDITOR` opens seeded with the mention, posts as a comment |
| detail | `f` | changed files |
| detail, files, diff | `v` | full PR diff in `$PAGER` |
| detail | `m` | raw markdown on/off |
| detail, diff | `/`, `n`/`N` | search, less-style: smartcase, next/previous, a `3/17` counter |
| detail, files, diff | `q`/`h`/esc | back (esc first clears a search) |
| files | `enter`/`l` | that file's diff in-app |
| fortress | `hjkl`/arrows, `HJKL`, `tab`/shift-`tab` | move the look cursor, by 8, or to the next/previous thing |
| fortress | `enter`, `y`, `o` | open the PR or issue under the cursor; copy or open its URL |
| fortress | `+`/`-`, `c` | zoom to a street a day / fit the city; the other city |
| prompts | enter / esc | submit / cancel. Ctrl-U, Ctrl-W, Ctrl-A/E, arrows, Home/End edit the line. A prompt with a minimum length (a reason) counts down and refuses enter until it is met |
| menus | `j`/`k` + enter, `1`–`9` | choose; esc cancels |

Esc never quits the list: Alt+key and some terminals' Ctrl+arrow sequences
begin with an escape, and a stray one used to quit the old dashboard.
