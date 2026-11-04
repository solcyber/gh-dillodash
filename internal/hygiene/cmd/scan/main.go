// Command scan runs the hygiene checks over text that is not in the tree:
// a pull request's title and body, commit messages. It reads stdin (or
// the files named) and exits 1 on a hit, printing only where it is and a
// hash prefix, never the text:
//
//	git log --format=%B base..head | go run ./internal/hygiene/cmd/scan -name "commit messages"
//
// Without $DILLODASH_HYGIENE_KEY only the structural shapes are checked,
// unless $DILLODASH_HYGIENE_REQUIRED=1, which makes it fail.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/hygiene"
)

func main() {
	name := flag.String("name", "stdin", "what the text is, for the report")
	flag.Parse()
	var m *hygiene.Matcher
	if key := strings.TrimSpace(os.Getenv(hygiene.KeyEnv)); key != "" {
		m = hygiene.NewMatcher(hygiene.Key(key), hygiene.Denylist())
	} else if os.Getenv(hygiene.RequiredEnv) == "1" {
		fmt.Fprintf(os.Stderr, "scan: %s is not set and %s=1\n", hygiene.KeyEnv, hygiene.RequiredEnv)
		os.Exit(2)
	} else {
		fmt.Fprintf(os.Stderr, "scan: %s is not set: structural checks only\n", hygiene.KeyEnv)
	}
	sources := map[string]io.Reader{*name: os.Stdin}
	order := []string{*name}
	if flag.NArg() > 0 {
		sources, order = map[string]io.Reader{}, nil
		for _, p := range flag.Args() {
			f, err := os.Open(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "scan: %v\n", err)
				os.Exit(2)
			}
			defer f.Close()
			sources[p] = f
			order = append(order, p)
		}
	}
	bad := 0
	for _, src := range order {
		data, err := io.ReadAll(sources[src])
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan: %s: %v\n", src, err)
			os.Exit(2)
		}
		for n, line := range strings.Split(string(data), "\n") {
			for _, re := range hygiene.Structural {
				if re.MatchString(line) {
					fmt.Printf("%s:%d: matches %s\n", src, n+1, re)
					bad++
				}
			}
		}
		if m != nil {
			for _, h := range m.Text(data) {
				fmt.Printf("%s:%d: private term (hash %s…)\n", src, h.Line, h.Hash[:12])
				bad++
			}
		}
	}
	if bad > 0 {
		fmt.Printf("%d hit(s); write it in the fictional vocabulary (see CONTRIBUTING)\n", bad)
		os.Exit(1)
	}
}
