package ui

// ShotCaptions is each gallery shot's caption (docs/shots/<name>.png):
// the alt text every doc embeds it with and docs/keys.md writes. The
// screenshot tool's manifest names the same shots; its test keeps the
// two and the docs in step.
var ShotCaptions = map[string]string{
	"list":                   "The list: pull requests with their review chips, CI glyphs, diffstat, author and age; the releases with a release in flight; the night's queue; the issues",
	"list-narrow":            "The list at eighty columns: the diffstat and author go first, the titles stay",
	"narrow-50x12":           "Fifty columns by twelve rows: number, chip and title, and a key bar down to ? help",
	"pr-detail":              "A pull request's detail: the header, the description rendered from markdown, then comments and reviews in order",
	"pr-files":               "The changed files of a pull request, with their status and diffstat; enter opens a file's diff",
	"pr-diff":                "One file's diff in-app, hunks colored; / searches it",
	"issue-detail":           "An issue's detail: labels, assignees, the body and the comments",
	"help":                   "The help overlay: every key of the current view by group, the workflows' keys, and the legend of chips",
	"filter":                 "The filter, live as you type: the rows narrow to the words",
	"goto":                   "Go to a number: #N, or repo#N when the repos share numbers",
	"terraform-approve":      "A terraform PR's detail with its plan cards: the plan id, what changes, the destroy count and the window verdict",
	"terraform-approve-menu": "a on a terraform PR: which plan to approve, with its destroys",
	"approve-menu":           "a on a release: queue it for tonight's window, or release NOW with a reason",
	"release-now-prompt":     "The release-now prompt: the reason, its length meter, and the exact comment that will be posted",
	"release-rail":           "A release in flight on the list: its rail fills phase by phase, with the phase and its clock",
	"release-card":           "Mission control for a release in flight: the T+ clock, the rail with Amos on it, one row per phase with its clock and its technical tail, the run",
	"release-launch":         "When a release lands: the rocket, the fireworks and RELEASED! across the screen",
	"waiting-runs":           "The waiting approvals section: Actions runs waiting on an environment with no listed PR behind them, approve: prod when you are a reviewer, waits: when someone else is",
	"waiting-runs-approve":   "D on a waiting run: the environments you review, confirmed before they are approved",
	"window-card":            "A PR queued for the maintenance window, opened: its plan card, disruptive and revertible",
	"fortress-chicago":       "Fortress mode over Chicago: Madison is today and older work lies south toward Indiana, Lake Michigan to the east",
	"fortress":               "Fortress mode over Manhattan: every repo as a lane of branches and pull requests, the oldest work furthest north",
	"splash":                 "The splash while the first refresh runs: the logotype cascades in and Amos walks",
	"amos":                   "Nothing to show: Amos walks the empty list under the stars",
	"icons-nerd":             "The list with the bundled Nerd Font's icons",
	"icons-unicode":          "The same list with icons = \"unicode\": plain Unicode symbols, no font needed",
	"icons-ascii":            "The same list with icons = \"ascii\": every glyph from the seven-bit set",
}
