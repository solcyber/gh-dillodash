// Package selfupdate knows which dillodash is running, which release is
// the latest, and every install on this machine: the stale-build
// banner, the release-action gate and `config check` read it.
package selfupdate

import (
	"strconv"
	"strings"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// Repo is where dillodash's releases are published.
var Repo = model.Repo{Owner: "solcyber", Name: "gh-dillodash"}

// parse reads "v1.2.3" (or "1.2.3") into its three numbers; ok false for
// anything else: a dev build ("dev+abc1234"), the demo ("v1.5.4 demo"),
// the tests ("test") and prereleases ("v1.6.0-rc1") are never compared.
func parse(v string) (n [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// Comparable reports whether v is a release version ("v1.2.3"): only
// those count as stale or new.
func Comparable(v string) bool {
	_, ok := parse(v)
	return ok
}

// Compare is -1, 0 or 1 as a is older than, the same as or newer than b.
// Anything not Comparable compares equal to everything.
func Compare(a, b string) int {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return 0
	}
	for i := range x {
		switch {
		case x[i] < y[i]:
			return -1
		case x[i] > y[i]:
			return 1
		}
	}
	return 0
}

// Newer reports whether latest is a newer release than running.
func Newer(latest, running string) bool {
	return Comparable(latest) && Comparable(running) && Compare(latest, running) > 0
}
