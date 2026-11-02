package ghapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/solcyber/gh-dillodash/internal/model"
)

func TestLatestReleaseConditional(t *testing.T) {
	s := newServer(t)
	s.handle("GET", "/repos/acme/tool/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v2"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v2"`)
		w.Write([]byte(`{"tag_name":"v1.5.4","draft":false,"prerelease":false,"body":"notes",
			"assets":[{"id":7,"name":"linux-amd64","size":3,"digest":"sha256:abc"}]}`))
	})
	c := s.client(t)
	repo := model.Repo{Owner: "acme", Name: "tool"}
	rel, etag, err := c.LatestRelease(context.Background(), repo, "")
	if err != nil || rel.Tag != "v1.5.4" || etag != `"v2"` || len(rel.Assets) != 1 || rel.Assets[0].Digest != "sha256:abc" {
		t.Fatalf("latest: %+v %q %v", rel, etag, err)
	}
	rel, etag, err = c.LatestRelease(context.Background(), repo, `"v2"`)
	if !errors.Is(err, ErrNotModified) || rel != nil || etag != `"v2"` {
		t.Errorf("conditional: %+v %q %v", rel, etag, err)
	}
	if _, _, err := c.LatestRelease(context.Background(), model.Repo{Owner: "acme", Name: "gone"}, ""); err == nil {
		t.Error("404 should be an error")
	}
}

func TestDownloadAsset(t *testing.T) {
	s := newServer(t)
	s.handle("GET", "/repos/acme/tool/releases/assets/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.Write([]byte("binary bytes"))
	})
	c := s.client(t)
	repo := model.Repo{Owner: "acme", Name: "tool"}
	var b strings.Builder
	if err := c.DownloadAsset(context.Background(), repo, 7, &b, 100); err != nil || b.String() != "binary bytes" {
		t.Fatalf("download: %q %v", b.String(), err)
	}
	b.Reset()
	if err := c.DownloadAsset(context.Background(), repo, 7, &b, 5); err == nil || !strings.Contains(err.Error(), "larger than 5 bytes") {
		t.Errorf("limit: %v", err)
	}
}
