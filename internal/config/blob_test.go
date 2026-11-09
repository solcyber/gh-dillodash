package config

import "testing"

func TestBlobSHAMatchesGit(t *testing.T) {
	if got := blobSHA([]byte("[reviews]\nrepos = [\"acme/infra\"]\n")); got != "12ddbbf84248d1b0ea56cc9ad6e8db8adce4b883" {
		t.Errorf("blobSHA = %s", got)
	}
}
