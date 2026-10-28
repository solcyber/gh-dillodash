# Contributing

dillodash is a Go program with no runtime dependencies beyond the GitHub
CLI. `go build ./...` and `go test ./...` are the whole workflow.

## Layout

```
main.go                     the gh extension entry point and subcommands
internal/config             the TOML config: schema, validation, migration
internal/ghapi              the GitHub client (REST + GraphQL through go-gh), rate limits, CI fallback
internal/model              PRs, issues, comments, checks, decision labels
internal/ui                 the Bubble Tea app: list, detail, files, diff, actions, keys
internal/canvas             the cell buffer every view paints into
internal/theme, glyphs      the palette and the three glyph sets
internal/markdown           the markdown renderer with workflow hooks
internal/widgets            prompts, confirms, menus
internal/scenes, fx, amos   splash, wipes, confetti, the launch, Amos
internal/fortress           fortress mode
internal/workflow           the workflow interface and the aggregator
internal/workflow/<name>    the optional workflows
internal/fonts              the embedded Nerd Font and its installer
internal/doctor             `config check`
cmd/gen-amos                regenerates the Amos sprite from assets/amos.png
```

## Tests

`go test ./...`. The UI tests drive the model offline on an acme fixture
through the fake GitHub client (`internal/ghapi/fake`); workflow tests use
the scripted app in `internal/workflow/wftest`. Any test that posts
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
what to do, name the key.

## Releasing

Tag `vX.Y.Z` on `main`. The release workflow builds the binaries for every
platform with `cli/gh-extension-precompile` and attaches them; `gh
extension upgrade dillodash` picks them up.
