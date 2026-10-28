# Parity checklist

dillodash is a port. This maps the original dashboard's test files to the
Go tests that pin the same behaviour, so nothing was dropped in the move.
Fixture names changed to the acme vocabulary; assertions did not.

| Original test | Covered | Go tests |
|---|---|---|
| `test_render.py`: cell widths, clip, pad | yes | `internal/text` |
| `test_render.py`: markdown, wrapping, tables, fences, inline, terraform coloring, summaries, comments | yes (91 subtests) | `internal/markdown` |
| `test_render.py`: WCAG contrast of every style on every background, mono bold set, 16-color mapping, chip caps | yes | `internal/theme` |
| `test_render.py`: glyph sets, nerd code points in the private-use range | yes | `internal/glyphs`, `internal/fonts` (cmap coverage) |
| `test_render.py`: list rows, filter, decision labels, CI clusters | yes | `internal/model` (`ci_test.go`), `internal/ui` (`ui_test.go`) |
| `test_render.py`: strobe rate of the launch, splash, wipes, confetti | yes | `internal/scenes` |
| `test_rate_limit.py` | yes | `internal/ghapi` (`Backoff`, `RetryAtFromHeaders`, GraphQL `RATE_LIMITED`) |
| `test_approve.py`: urlopen errors, truncated bodies, 403 hints | yes | `internal/ghapi`, `internal/deploys` |
| `test_approve.py`: prompt editing (^U, ^W, wide chars), confirm, menus, reason meter | yes | `internal/widgets` |
| `test_approve.py`: editor round trip, clipboard, open URL | yes | `internal/external` |
| `test_approve.py`: the generic approve, comment, tag, merge and deploy flows | yes | `internal/ui` (`ui_test.go`), `internal/deploys` |
| `test_approve.py`: `/approve` bodies, refusals, plan-sets, ceremonies, `--outside-window` | workflow port | `internal/workflow/terraformapprove` |
| `test_plan_candidates.py`, `test_adr0138.py` (plan liveness, sets, ceremonies, apply outcomes) | workflow port | `internal/workflow/terraformapprove` |
| `test_window_queue.py` | workflow port | `internal/workflow/maintenancewindow` |
| `test_approve.py`: release approve, `M` in and out of the window, `D`, stage units | workflow port | `internal/workflow/releasepr` |
| `test_render.py`, `test_tui.py`: fortress mode | fortress port | `internal/fortress`, `internal/ui/fortress_test.go` |
| `test_tui.py`: end-to-end on a pty across tiers and glyph sets | partly | `internal/ui/ui_test.go` drives the model offline; a pty run is `go run . --demo` |
| `test_install.py`: the font installer's platform matrix | yes | `internal/fonts` |
| `test_fonts.py`: checksums, glyph coverage | yes | `internal/fonts` |
| config parsing (the old `[defaults]`/`[user.*]`/`[releases]` loader) | yes, new schema | `internal/config` |
| the validator conformance checks (four checks against the private validator) | opt-in | `DILLODASH_VALIDATOR=<path>` gates them in the terraform workflow tests |

Behaviour that is new in dillodash and has no original test: multi-repo
lists (`TestMultiRepoRows`), `config check`, `config migrate`, the
no-private-terms sweep (`internal/hygiene`).
