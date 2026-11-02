package ghapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/solcyber/gh-dillodash/internal/model"
)

// Release is a published GitHub release, as far as the build check
// reads it.
type Release struct {
	Tag         string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	HTMLURL     string         `json:"html_url"`
	PublishedAt time.Time      `json:"published_at"`
	Assets      []ReleaseAsset `json:"assets"`
}

// ReleaseAsset is one file attached to a release. Digest is GitHub's
// "sha256:<hex>" of the uploaded bytes.
type ReleaseAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// ErrNotModified is LatestRelease's answer to a conditional request
// whose ETag still matches: nothing changed since that answer.
var ErrNotModified = errors.New("not modified")

// LatestRelease reads the repo's latest published release (GitHub never
// answers a draft or a prerelease here). With etag, the request is
// conditional: an unchanged release answers ErrNotModified, which
// costs no rate limit. The new ETag comes back with the release.
func (c *GitHub) LatestRelease(ctx context.Context, repo model.Repo, etag string) (*Release, string, error) {
	var hdr map[string]string
	if etag != "" {
		hdr = map[string]string{"If-None-Match": etag}
	}
	rawURL := c.restURL(repoPath(repo, "releases/latest"))
	body, h, err := c.doH(ctx, http.MethodGet, rawURL, nil, acceptJSON, hdr)
	if err != nil {
		if e, ok := AsError(err); ok && e.Status == http.StatusNotModified {
			return nil, etag, ErrNotModified
		}
		return nil, "", err
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, "", &Error{Method: http.MethodGet, URL: rawURL, Message: "bad response: " + err.Error()}
	}
	return &rel, h.Get("ETag"), nil
}

// downloadTimeout bounds an asset download: a release binary is tens of
// megabytes, far past the per-request timeout of an API call.
const downloadTimeout = 10 * time.Minute

// DownloadAsset streams a release asset into w through the API (so a
// private repo's asset works with the token), refusing more than limit
// bytes. GitHub redirects to its storage host; the token goes only to
// the API host.
func (c *GitHub) DownloadAsset(ctx context.Context, repo model.Repo, id int64, w io.Writer, limit int64) error {
	rawURL := c.restURL(repoPath(repo, fmt.Sprintf("releases/assets/%d", id)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return &Error{Method: http.MethodGet, URL: rawURL, Message: "bad request: " + err.Error()}
	}
	req.Header.Set("Accept", "application/octet-stream")
	hc := *c.http
	hc.Timeout = downloadTimeout
	resp, err := hc.Do(req)
	if err != nil {
		return &Error{Method: http.MethodGet, URL: rawURL, Message: "network error: " + netErrorText(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(http.MethodGet, rawURL, resp)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return &Error{Method: http.MethodGet, URL: rawURL, Message: "network error: " + netErrorText(err)}
	}
	if n > limit {
		return &Error{Method: http.MethodGet, URL: rawURL, Message: fmt.Sprintf("the asset is larger than %d bytes", limit)}
	}
	return nil
}
