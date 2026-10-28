package config

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// Source is where a pulled config came from, recorded in a header at the
// top of the installed file so `gh dillodash config pull` (no argument)
// can refresh it and `config check` can say whether it is current.
type Source struct {
	Repo model.Repo
	Path string    // file path inside the repo (default "config.toml")
	SHA  string    // the blob sha GitHub reported when it was pulled
	At   time.Time // when it was pulled
}

// DefaultPullPath is the file pulled when a spec names only the repo.
const DefaultPullPath = "config.toml"

// Spec is "owner/repo:path".
func (s Source) Spec() string { return s.Repo.String() + ":" + s.Path }

// ParseSpec reads "owner/repo" or "owner/repo:path/in/repo.toml".
func ParseSpec(spec string) (Source, error) {
	spec = strings.TrimSpace(spec)
	repoPart, path := spec, DefaultPullPath
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		repoPart, path = spec[:i], strings.Trim(spec[i+1:], "/")
	}
	repo, ok := model.ParseRepo(repoPart)
	if !ok {
		return Source{}, fmt.Errorf("%q is not owner/repo or owner/repo:path", spec)
	}
	if path == "" || strings.Contains(path, "..") {
		return Source{}, fmt.Errorf("%q: the path must be a file inside the repo", spec)
	}
	return Source{Repo: repo, Path: path}, nil
}

const (
	headerPrefix = "# dillodash: pulled from "
	headerHint   = "# refresh with: gh dillodash config pull"
)

var headerRE = regexp.MustCompile(`^# dillodash: pulled from (\S+) @ ([0-9a-f]{7,40}) on (\S+)$`)

// Stamp prefixes body with the source header (replacing an existing one).
func Stamp(s Source, body []byte) []byte {
	body = StripHeader(body)
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s%s @ %s on %s\n%s\n", headerPrefix, s.Spec(), s.SHA, s.At.UTC().Format(time.RFC3339), headerHint)
	b.Write(body)
	return b.Bytes()
}

// StripHeader removes the source header, if any.
func StripHeader(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte(headerPrefix)) {
		return data
	}
	lines := bytes.SplitN(data, []byte("\n"), 3)
	if len(lines) >= 2 && string(lines[1]) == headerHint {
		if len(lines) == 3 {
			return lines[2]
		}
		return nil
	}
	if len(lines) >= 2 {
		return bytes.Join(lines[1:], []byte("\n"))
	}
	return nil
}

// ParseSource reads the source header from an installed config.
func ParseSource(data []byte) (Source, bool) {
	first, _, _ := bytes.Cut(data, []byte("\n"))
	m := headerRE.FindStringSubmatch(strings.TrimRight(string(first), "\r"))
	if m == nil {
		return Source{}, false
	}
	src, err := ParseSpec(m[1])
	if err != nil {
		return Source{}, false
	}
	src.SHA = m[2]
	if t, err := time.Parse(time.RFC3339, m[3]); err == nil {
		src.At = t
	}
	return src, true
}
