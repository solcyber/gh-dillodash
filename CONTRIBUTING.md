# Contributing

dillodash is a Go program with no runtime dependencies beyond the GitHub
CLI. `go build ./...` and `go test ./...` are the whole workflow; CI also
runs the tests with `-race`, `gofmt -l`, `go mod tidy -diff`,
`staticcheck` (`go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...`)
and `govulncheck`, so run those before a pull request.

## Layout

```
main.go                     the gh extension entry point and subcommands
internal/config             the TOML config: schema, validation, migration
internal/ghapi              the GitHub client (REST + GraphQL through go-gh), rate limits, CI fallback
internal/model              PRs, issues, comments, checks, decision labels
internal/ui                 the Bubble Tea app: list, detail, files, diff, actions; keys.go is the key table
internal/canvas             the cell buffer every view paints into
internal/text               cell-width text: clipping, ellipses, segments
internal/theme, glyphs      the palette and the three glyph sets
internal/markdown           the markdown renderer with workflow hooks
internal/widgets            prompts, confirms, menus
internal/scenes, fx, amos   splash, wipes, confetti, the launch, Amos
internal/fortress           fortress mode
internal/clock              the clock (one test patch point) and age formatting
internal/deploys            the deploy state of a merged PR's Actions runs
internal/external           $EDITOR, $PAGER, the clipboard, the browser
internal/workflow           the workflow interface and the aggregator
internal/workflow/<name>    the optional workflows
internal/fonts              the embedded Nerd Font, its licences and the installer
internal/doctor             `config check`
internal/demo               the offline fixture behind --demo and the screenshot
internal/hygiene            the private-terms test over the whole tree
cmd/gen-amos                regenerates the Amos sprite from assets/amos.png (rendered from assets/amos-rough.pdf)
cmd/screenshot              renders docs/screenshot.png from the demo fixture
```

Two files are generated and checked by tests: `config.example.toml` at the
root is a copy of `internal/config/example.toml` (edit the latter, then
`go test ./internal/config -update`), and `docs/keys.md` is rendered from
the key table in `internal/ui/keys.go` (`go test ./internal/ui -update`).

## Tests

`go test ./...`. The UI tests drive the model offline on an acme fixture
through the fake GitHub client (`internal/ghapi/fake`); workflow tests use
the scripted app in `internal/workflow/wftest`. A test must never start a
program on the developer's machine: the browser, the clipboard tool and
`$PAGER`/`$EDITOR` go through the model's `testPrompter` hooks, which
`newHarness` scripts (a key that reached the real `open` once filled a
browser with `acme` tabs on every run). Any test that posts
something asserts the exact body, because the bots on the other side
validate it.

Fixtures use a fictional vocabulary: org `acme`, repos `acme/infra`,
`acme/web-portal`, `acme/etl-jobs`, `acme/db-migrations`, people `alice`,
`bob`, `carol`, `mallory`, bot `infra-bot`. Please keep it; real
organizations, people and incidents do not belong in this repository, and
a test (`TestNoPrivateTerms`) checks for a denylist the maintainers hold.

## Running it against a fake

`go run . --demo` opens the dashboard on the fixture without touching
GitHub. `go run ./cmd/screenshot` renders the same fixture offline with
the embedded font and the theme's colors into `docs/screenshot.png` (the
list, then a pull request's detail); regenerate it after a visible change.

## Style

`gofmt`, `go vet`, short doc comments that say why. Status messages and
prompts are sentences a person reads under pressure: say what happened and
what to do, name the key. The conventions a test partly enforces:

- Status, notice and error text starts lowercase (proper nouns and keys
  keep their case), has no trailing period, and fits one line; `e` shows
  a long one whole.
- An aside is set off with an em dash (` — `), an instruction follows a
  semicolon, a cause or value a colon. Never ` - `.
- "PR" in status text; "pull request" in prose. Named keys read as on
  the keycap in prose (Esc, Enter, Ctrl-U) and lowercase in the key bar.
  Acronyms keep their case (URL, CI, SHA). "couldn't", not "could not".
- GitHub enums are humanized (`changes requested`); short SHAs are seven
  characters (`model.ShortSHA`).

A new key goes in the table in `internal/ui/keys.go`: the key bar, the
help overlay and `docs/keys.md` follow from it.

## Releasing

Add the version's section to `CHANGELOG.md` (the release notes are taken
from it), then tag `vX.Y.Z` on `main`. The release workflow runs the test
matrix, builds the binaries for every platform with
`cli/gh-extension-precompile`, attaches them with build attestations, and
`gh extension upgrade dillodash` picks them up. Regenerate
`docs/screenshot.png` first when the list or the detail changed.
