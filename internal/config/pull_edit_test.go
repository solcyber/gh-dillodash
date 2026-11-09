package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// TestEdited: a pulled body is not edited; a changed one is; an
// unstamped file never is.
func TestEdited(t *testing.T) {
	body := []byte("[reviews]\nrepos = [\"acme/infra\"]\n")
	// git hash-object of body
	src := Source{Repo: model.Repo{Owner: "acme", Name: "cfg"}, Path: "config.toml", SHA: "", At: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	src.SHA = blobSHA(body)
	stamped := Stamp(src, body)
	if Edited(stamped) {
		t.Error("an untouched pulled config reads edited")
	}
	if !Edited([]byte(strings.Replace(string(stamped), "acme/infra", "acme/web", 1))) {
		t.Error("a hand edit is not noticed")
	}
	if Edited(body) {
		t.Error("an unstamped file reads edited")
	}
}

// TestBackupNeverReplacesTheFirst: the first copy is .bak, later ones
// are stamped, so a second overwrite keeps the original hand-written one.
func TestBackupNeverReplacesTheFirst(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte("one"), 0o600)
	now := time.Date(2026, 11, 9, 15, 30, 0, 0, time.UTC)
	if d, err := Backup(p, now); err != nil || d != p+".bak" {
		t.Fatalf("first backup %q %v", d, err)
	}
	os.WriteFile(p, []byte("two"), 0o600)
	d, err := Backup(p, now)
	if err != nil || d != p+".bak.20261109T153000Z" {
		t.Fatalf("second backup %q %v", d, err)
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != "one" {
		t.Errorf(".bak = %q, want the first file", b)
	}
}

// TestInstallPathIsTheLoadedFile: with $XDG_CONFIG_HOME set but the
// config in ~/.config, init/pull write where the loader reads.
func TestInstallPathIsTheLoadedFile(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	legacy := filepath.Join(home, ".config", "dillodash", "config.toml")
	os.MkdirAll(filepath.Dir(legacy), 0o700)
	os.WriteFile(legacy, []byte("[reviews]\n"), 0o600)
	env := func(k string) string {
		switch k {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return xdg
		}
		return ""
	}
	if got := InstallPath(env); got != legacy {
		t.Errorf("InstallPath = %q, want the loaded %q", got, legacy)
	}
}
