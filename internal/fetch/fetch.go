// Package fetch does the loader's HTTP: GETs with the loader's User-Agent,
// checksummed downloads, and repository keys that only ever go to their own
// origin (re-checked on every redirect).
package fetch

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var Client = &http.Client{Timeout: 10 * time.Minute}

// UserAgent is sent with every request ("FrogLoader/<version>", set by main).
var UserAgent = "FrogLoader"

// MaxDownload caps any single download.
const MaxDownload = 4 << 30

// StatusError is a non-200 reply.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Code) }

func Get(ctx context.Context, url string) (*http.Response, error) {
	return GetAuth(ctx, url, nil)
}

// GetAuth is Get that adds a repository key where it may go (see
// Auth): on the request and on redirects, re-checked for every hop.
func GetAuth(ctx context.Context, url string, auth *Auth) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	auth.Apply(req)
	client := *Client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		r.Header.Del("Authorization")
		auth.Apply(r)
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &StatusError{URL: url, Code: resp.StatusCode}
	}
	return resp, nil
}

// Checksum to verify a download against.
type Checksum struct {
	Algo string // "sha256" or "md5"
	Hex  string
	Size int64 // 0 = unknown
}

// Download fetches url into dest (created/truncated), verifying the checksum.
// On failure dest is removed.
func Download(ctx context.Context, url, dest string, sum Checksum) error {
	return DownloadAuth(ctx, url, dest, sum, nil)
}

// DownloadAuth is Download with a repository key (sent only to the
// repository's own origin).
func DownloadAuth(ctx context.Context, url, dest string, sum Checksum, auth *Auth) error {
	if sum.Hex == "" && sum.Algo != "none" {
		return errors.New("no checksum for " + url)
	}
	resp, err := GetAuth(ctx, url, auth)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	var h hash.Hash = sha256.New()
	if strings.EqualFold(sum.Algo, "md5") {
		h = md5.New()
	}
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, MaxDownload))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && sum.Size > 0 && n != sum.Size {
		err = fmt.Errorf("download size %d, expected %d", n, sum.Size)
	}
	if err == nil && sum.Algo != "none" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum.Hex) {
			err = fmt.Errorf("checksum mismatch for %s (got %s, expected %s)", url, got, sum.Hex)
		}
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}

// Auth carries a repository key and the only origin it may be sent to.
type Auth struct {
	Origin *url.URL
	Key    string
}

func SameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// apply adds "Authorization: Bearer <key>" only for the repository's origin.
func (ra *Auth) Apply(req *http.Request) {
	if ra == nil || ra.Key == "" || ra.Origin == nil {
		return
	}
	if SameOrigin(req.URL, ra.Origin) {
		req.Header.Set("Authorization", "Bearer "+ra.Key)
	}
}
