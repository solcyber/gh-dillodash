package config

import _ "embed"

// exampleTOML is the annotated reference config. The repo-root
// config.example.toml is a verbatim copy; TestExampleMatchesRepoRoot
// keeps the two in sync (run `go test ./internal/config -update` to
// regenerate the root file from this one).
//
//go:embed example.toml
var exampleTOML string

// Example returns the full commented config.example.toml. It parses with
// no errors and no warnings.
func Example() string { return exampleTOML }
