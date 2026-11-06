# Keys

<!-- generated from internal/ui/keys.go by `go test ./internal/ui -update`; edit the table, not this file -->

| View | Key | Action |
|---|---|---|
| list, files | `j` / `k` | move the cursor (arrows too) |
| detail, diff | `j` / `k` | scroll a line (arrows too) |
| list, detail, files, diff | `g` / `G` / home / end | top / bottom |
| list, detail, files, diff | pgdn / pgup / space | a page down / up |
| list | enter / `l` | open the PR or issue (`l` and → too) |
| files | enter / `l` | that file's diff in-app (`l` and → too) |
| list | `/` | filter, live as you type: words match the number, title, author, labels and a workflow's own text (a stack, a repo) |
| list | esc | clear the filter (Esc never quits: a stray escape from an Alt chord must not close the dashboard) |
| list | `A` | toggle PRs approved by others (issues are always all shown) |
| list | `D` | approve the deployments waiting on the selected merged PR, or on the selected waiting run (the bar shows it while `release_pr` or `waiting_runs` is on) |
| list | `x` | the Actions runs of the selected row (a release's run, a waiting run, the runs on a PR's commit): re-run the failed jobs or every job, cancel, reject the deployment it waits on, or read a job's log in `$PAGER` |
| list, detail | `X` | run a workflow the config lists under `[[workflows.dispatch]]`: pick it, fill its inputs, confirm the exact payload; the status line follows the new run (the bar shows it while one is listed) |
| list | `P` | pull the newer config from the repo it was pulled from (`config pull`) and restart into it, keeping your place (the bar shows it while the header says the config is outdated) |
| list, detail | `u` | open the compare view from a stale approval's commit to the head: what changed since the PR was approved, so a re-approve is a look at the delta (the bar shows it while the selected PR's approval is on an older commit) |
| list | `d` | fortress mode: a Dwarf Fortress map of PRs, issues, branches and commits |
| detail | `a` | approve: a workflow may take over (`/approve` with a plan id, a release's queue-or-now choice, or release NOW alone on a release you already approved); otherwise a GitHub review approval after a confirm |
| detail | `M` | merge the open, approved PR, naming its head so a branch that moved is refused by GitHub; a workflow may refuse or ask its own question first |
| detail | `D` | approve the environment deployments waiting on the open merged PR |
| detail | `x` | the Actions runs of the open PR (its head's, or its merge's once merged, and a release's run): re-run, cancel, reject the deployment it waits on, or read a job's log in `$PAGER` |
| detail | `c` / `C` | comment inline / in `$EDITOR` |
| detail | `t` | tag someone: pick a repo user, `$EDITOR` opens seeded with the mention, posts as a comment |
| detail | `L` | labels: pick one of the repo's labels to add, or one the item carries (`[x]`) to remove |
| issue | `s` | assign an issue: pick a repo user to assign, or an assignee (`[x]`) to unassign |
| issue | `Z` | close an open issue as completed or as not planned; reopen a closed one (after a confirm) |
| detail | `H` | hand the open PR or issue to the agent: adds `[agent] handoff_label` and, when set, posts `handoff_comment`, after a confirm (an agent that is a GitHub App's bot watches a label, not @-mentions; the bar shows it while `handoff_label` is set) |
| detail | `f` | changed files |
| detail | `v` | the full PR diff in `$PAGER` |
| detail | `/` | search, less-style: smartcase, a `3/17` counter |
| detail | `n` / `N` | next / previous match |
| detail | `m` | raw markdown on/off |
| detail | `q` / `h` / esc | back to the list (`h`, ← and Esc too; Esc first clears a search) |
| files | `v` | the whole PR diff in `$PAGER` |
| files | `r` | reload the file list (not a dashboard refresh) |
| files | `q` / `h` / esc | back to the PR (`h`, ← and Esc too) |
| diff | `/` | search the diff, less-style |
| diff | `n` / `N` | next / previous match |
| diff | `v` | the whole PR diff in `$PAGER` |
| diff | `q` / `h` / esc | back to the files (`h`, ← and Esc too; Esc first clears a search) |
| fortress | `h` / `j` / `k` / `l` | move the look cursor (arrows too) |
| fortress | `H` / `J` / `K` / `L` | move the look cursor by 8 |
| fortress | tab / shift+tab | jump to the next / previous thing |
| fortress | enter | open the PR or issue under the cursor |
| fortress | `+` / `-` / `=` | zoom to a street a day / fit the city (`=` is `+`) |
| fortress | `c` | the next city: Manhattan, Chicago, the Kowloon Walled City, round again |
| fortress | `b` | show / hide the map's epigraph, Borges's *On Exactitude in Science*, laid where it covers the least of the city (`[display] fortress_epigraph` sets where it starts) |
| fortress | `d` / `q` / esc | back to the list (`q` and Esc too) |
| list | `y` | copy the URL (tmux buffer, the system clipboard, or OSC 52); the status line says which |
| detail, files, diff, fortress | `y` | copy the open PR's, issue's or file's URL, as in the list |
| list, detail, files, diff, fortress | `o` | open the URL in a browser when this host has one; otherwise like `y` |
| list, detail, diff, fortress | `r` | refresh now (also re-checks CI that already finished, and re-tries a repo whose token was fixed) |
| list, detail | `R` | restart into the dillodash release installed in the background, keeping the view, the selected row and the open PR; it waits for an action in flight (the bar shows it once one is installed) |
| any | `W` | the room: while the release window is open the dashboard is the view out of a window, behind the glass in the middle of a room, as a reminder; `W` steps out to the plain dashboard and back in (the plaque on the wall says so, and the bar shows `W` while you are out). The next window opens in the room again. Outside the window the dashboard is plain and `W` steps into the room any time (the bar shows `W` there too, among the first chips to go when the bar is short) |
| list | `q` | quit (press twice while an action is still talking to GitHub) |
| any | `:` | go to `#N`, `repo#N` or `owner/repo#N`: a PR first, then an issue; a bare `#N` opens the listed row with that number in any repo (release rows too), and asks which when several repos list it |
| any | `?` | help: every key, the glyph legend, the version; `j`/`k` scroll it when it overflows |
| any | `e` | the last status or error message in full (the status line cuts a long one with an ellipsis) |
| any | ctrl+c | quit (interrupt); inside a prompt it cancels the prompt |
| list | `a` | with `release_pr`, on a release's row: what `a` does in its detail — release NOW alone on a release you approved (or one the window bumped), else approve with the queue-or-now choice |
| list, detail | `w` | with `maintenance_window`: `/attend`, `/release` or `/window-cancel` on the night's manifest issue, or open it |
| list | `p` | with `promote`, on a change's row: move it one tier on — open its promotion PR from the first tier, merge the open promotion PR, or tag the staging unit go or hold |
| prompts | enter / esc | submit / cancel (Ctrl-C and Ctrl-G cancel too). Ctrl-U, Ctrl-W, Ctrl-K, Ctrl-A/E, arrows, Home/End edit the line; a paste inserts as one edit. A prompt with a minimum length (a reason) counts down and refuses enter until it is met |
| menus | `j`/`k` + enter, `1`–`9` | choose; esc, `q`, Ctrl-C cancel |
| confirms | `y` | anything else is no |

Esc never quits the list: Alt+key and some terminals' Ctrl+arrow sequences
begin with an escape, so a stray one must never quit.

## The screens

Rendered from the offline demo fixture by `go run ./cmd/screenshot`.

**The list**

![The list: pull requests with their review chips, CI glyphs, diffstat, author and age; the releases with a release in flight; the night's queue; the issues](shots/list.png)

**`/` filters as you type**

![The filter, live as you type: the rows narrow to the words](shots/filter.png)

**`:` goes to a number**

![Go to a number: #N, or repo#N when the repos share numbers](shots/goto.png)

**A pull request's detail**

![A pull request's detail: the header, the description rendered from markdown, then comments and reviews in order](shots/pr-detail.png)

**An issue's detail**

![An issue's detail: labels, assignees, the body and the comments](shots/issue-detail.png)

**`f`: the changed files**

![The changed files of a pull request, with their status and diffstat; enter opens a file's diff](shots/pr-files.png)

**enter on a file: its diff**

![One file's diff in-app, hunks colored; / searches it](shots/pr-diff.png)

**`?`: the help overlay**

![The help overlay: every key of the current view by group, the workflows' keys, and the legend of chips](shots/help.png)

**`d`: fortress mode**

![Fortress mode over Manhattan: every repo as a lane of branches and pull requests, the oldest work furthest north](shots/fortress.png)
