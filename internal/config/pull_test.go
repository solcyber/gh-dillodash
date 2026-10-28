package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in, repo, path string
		ok             bool
	}{
		{"acme/dash-config", "acme/dash-config", "config.toml", true},
		{"acme/dash-config:team/config.toml", "acme/dash-config", "team/config.toml", true},
		{" acme/x:/a.toml ", "acme/x", "a.toml", true},
		{"acme", "", "", false},
		{"acme/x:../etc", "", "", false},
		{"acme/x:", "", "", false},
	}
	for _, c := range cases {
		s, err := ParseSpec(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ParseSpec(%q) err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (s.Repo.String() != c.repo || s.Path != c.path) {
			t.Errorf("ParseSpec(%q) = %s %s", c.in, s.Repo, s.Path)
		}
	}
}

func TestStampRoundTrip(t *testing.T) {
	body := []byte("[reviews]\nrepos = [\"acme/infra\"]\n")
	src := Source{Path: "config.toml", SHA: "abc1234abc1234abc1234abc1234abc1234abc12", At: time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC)}
	src.Repo.Owner, src.Repo.Name = "acme", "dash-config"
	out := Stamp(src, body)
	if !strings.HasPrefix(string(out), "# dillodash: pulled from acme/dash-config:config.toml @ abc1234abc1234abc1234abc1234abc1234abc12 on 2026-10-28T19:00:00Z\n# refresh with: gh dillodash config pull\n[reviews]") {
		t.Fatalf("stamped:\n%s", out)
	}
	got, ok := ParseSource(out)
	if !ok || got.Spec() != src.Spec() || got.SHA != src.SHA || !got.At.Equal(src.At) {
		t.Errorf("ParseSource = %+v %v", got, ok)
	}
	if string(StripHeader(out)) != string(body) {
		t.Errorf("StripHeader = %q", StripHeader(out))
	}
	// stamping twice does not stack headers
	if again := Stamp(src, out); string(again) != string(out) {
		t.Errorf("double stamp differs:\n%s", again)
	}
	// the stamped file still parses with no warnings
	if c, err := Parse(out, "x.toml", Overrides{}); err != nil || len(c.Warnings) != 0 {
		t.Errorf("stamped config: %v %v", err, c)
	}
	if _, ok := ParseSource(body); ok {
		t.Error("a plain config has no source")
	}
}
