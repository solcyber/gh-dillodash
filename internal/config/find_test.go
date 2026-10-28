package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(minimal), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindPath(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "xdg")
	explicit := filepath.Join(t.TempDir(), "explicit.toml")
	envVar := filepath.Join(t.TempDir(), "env.toml")
	xdgCfg := filepath.Join(xdg, "dillodash", "config.toml")
	homeCfg := filepath.Join(home, ".config", "dillodash", "config.toml")
	legacy := filepath.Join(home, ".config", "pr-dash", "config.ini")

	env := func(m map[string]string) func(string) string {
		base := map[string]string{"HOME": home}
		for k, v := range m {
			base[k] = v
		}
		return func(k string) string { return base[k] }
	}

	// nothing anywhere: no path, no legacy, no error
	p, ini, err := FindPath("", env(nil))
	if p != "" || ini != "" || err != nil {
		t.Fatalf("empty home: %q %q %v", p, ini, err)
	}

	// a legacy INI alone is reported
	touch(t, legacy)
	p, ini, err = FindPath("", env(nil))
	if p != "" || ini != legacy || err != nil {
		t.Fatalf("legacy: %q %q %v", p, ini, err)
	}

	// ~/.config/dillodash/config.toml wins over the INI
	touch(t, homeCfg)
	p, ini, err = FindPath("", env(nil))
	if p != homeCfg || ini != "" || err != nil {
		t.Fatalf("home: %q %q %v", p, ini, err)
	}

	// XDG_CONFIG_HOME set but without a file falls back to ~/.config
	p, _, err = FindPath("", env(map[string]string{"XDG_CONFIG_HOME": xdg}))
	if p != homeCfg || err != nil {
		t.Fatalf("xdg unset file: %q %v", p, err)
	}
	// XDG_CONFIG_HOME with a file wins
	touch(t, xdgCfg)
	p, _, err = FindPath("", env(map[string]string{"XDG_CONFIG_HOME": xdg}))
	if p != xdgCfg || err != nil {
		t.Fatalf("xdg: %q %v", p, err)
	}

	// $DILLODASH_CONFIG beats both; missing is an error
	_, _, err = FindPath("", env(map[string]string{"XDG_CONFIG_HOME": xdg, EnvVar: envVar}))
	if err == nil || !strings.Contains(err.Error(), envVar+": not found ($DILLODASH_CONFIG)") {
		t.Fatalf("env missing: %v", err)
	}
	touch(t, envVar)
	p, ini, err = FindPath("", env(map[string]string{"XDG_CONFIG_HOME": xdg, EnvVar: envVar}))
	if p != envVar || ini != "" || err != nil {
		t.Fatalf("env: %q %q %v", p, ini, err)
	}

	// --config beats everything; missing is an error, a directory is not a file
	_, _, err = FindPath(explicit, env(map[string]string{EnvVar: envVar}))
	if err == nil || err.Error() != explicit+": not found" {
		t.Fatalf("explicit missing: %v", err)
	}
	_, _, err = FindPath(home, env(nil))
	if err == nil {
		t.Fatal("a directory is not a config")
	}
	touch(t, explicit)
	p, ini, err = FindPath(explicit, env(map[string]string{EnvVar: envVar}))
	if p != explicit || ini != "" || err != nil {
		t.Fatalf("explicit: %q %q %v", p, ini, err)
	}
}

func TestDefaultPath(t *testing.T) {
	if got := DefaultPath(fakeEnv(map[string]string{"HOME": "/home/alice"})); got != "/home/alice/.config/dillodash/config.toml" {
		t.Errorf("DefaultPath = %q", got)
	}
	if got := DefaultPath(fakeEnv(map[string]string{"HOME": "/home/alice", "XDG_CONFIG_HOME": "/xdg"})); got != "/xdg/dillodash/config.toml" {
		t.Errorf("DefaultPath xdg = %q", got)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if _, err := Load(p, Overrides{}); err == nil || err.Error() != p+": not found" {
		t.Fatalf("missing: %v", err)
	}
	touch(t, p)
	c, err := Load(p, Overrides{})
	if err != nil || c.Path != p || len(c.Reviews.Repos) != 1 {
		t.Fatalf("Load: %v %+v", err, c)
	}
	if err := os.WriteFile(p, []byte("[reviews]\nrepos = [\"acme\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, Overrides{}); err == nil || !strings.HasPrefix(err.Error(), p+": [reviews] repos:") {
		t.Fatalf("Load error: %v", err)
	}
}
