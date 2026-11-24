package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWhatsNewOnce: a machine that never ran 2.x sees the 2.0 keys once
// (and not the 2.7 card after them); one that knows 2.0 sees the 2.7 card
// once; then nothing.
func TestWhatsNewOnce(t *testing.T) {
	fresh := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", fresh)
	if got := whatsNewOnce(); got != whatsNew20 {
		t.Fatalf("a fresh machine: %.30q", got)
	}
	if got := whatsNewOnce(); got != "" {
		t.Errorf("then nothing: %.30q", got)
	}
	known := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", known)
	if err := os.MkdirAll(filepath.Join(known, "dillodash"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(known, "dillodash", "seen-2.0"), []byte("2.6.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := whatsNewOnce(); got != whatsNew27 {
		t.Fatalf("a 2.x machine: %.30q", got)
	}
	if got := whatsNewOnce(); got != "" {
		t.Errorf("then nothing: %.30q", got)
	}
}
