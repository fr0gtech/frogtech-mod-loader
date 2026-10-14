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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
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
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, retryAfter, err := getOnce(ctx, url, auth)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt >= len(RetryDelays) || !retryable(err) || ctx.Err() != nil {
			return nil, lastErr
		}
		wait := RetryDelays[attempt]
		if retryAfter > 0 {
			wait = min(retryAfter, maxRetryAfter)
		}
		log.Printf("%v; trying again in %s", err, wait)
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(wait):
		}
	}
}

// RetryDelays are the waits between attempts when a server or the network
// hiccups (502/503/504, 429, refused or reset connections, timeouts): a
// short outage, such as a site redeploying, never shows up as an error.
var RetryDelays = []time.Duration{1 * time.Second, 3 * time.Second, 7 * time.Second}

const maxRetryAfter = 10 * time.Second

func getOnce(ctx context.Context, url string, auth *Auth) (*http.Response, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
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
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, retryAfterOf(resp), &StatusError{URL: url, Code: resp.StatusCode}
	}
	return resp, 0, nil
}

// retryable: transient server answers and network failures. Other 4xx
// (wrong key, not found) are final.
func retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusTooManyRequests:
			return true
		}
		return false
	}
	var ne net.Error
	var oe *net.OpError
	if errors.As(err, &oe) && (oe.Op == "dial" || oe.Op == "read") {
		return true // refused/reset/unreachable, incl. Windows' WSA error codes
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		(errors.As(err, &ne) && ne.Timeout())
}

// retryAfterOf reads Retry-After (seconds or an HTTP date) on 429/503.
func retryAfterOf(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
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
