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
	return get(ctx, Client, url, auth, 0)
}

// get is GetAuth on a given client; from > 0 asks for the rest of the file
// from that byte on (the reply is then 206, or 200 with the whole file when
// the server doesn't do ranges).
func get(ctx context.Context, client *http.Client, url string, auth *Auth, from int64) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, retryAfter, err := getOnce(ctx, client, url, auth, from)
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

func getOnce(ctx context.Context, base *http.Client, url string, auth *Auth, from int64) (*http.Response, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
	}
	auth.Apply(req)
	client := *base
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
	if resp.StatusCode != http.StatusOK && !(from > 0 && resp.StatusCode == http.StatusPartialContent) {
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
	return Job{URL: url, Dest: dest, Sum: sum}.Run(ctx)
}

// DownloadAuth is Download with a repository key (sent only to the
// repository's own origin).
func DownloadAuth(ctx context.Context, url, dest string, sum Checksum, auth *Auth) error {
	return Job{URL: url, Dest: dest, Sum: sum, Auth: auth}.Run(ctx)
}

// DownloadClient does the file downloads. It has no time limit for the whole
// transfer (a big file on a slow server may take long); StallTimeout ends a
// transfer that stopped moving.
var DownloadClient = &http.Client{}

// StallTimeout: a download that got no data for this long is cut off and
// continued with a new request.
var StallTimeout = 45 * time.Second

type progressKey struct{}

// WithProgress makes downloads under ctx report their byte counts (total is
// 0 when the size isn't known).
func WithProgress(ctx context.Context, fn func(done, total int64)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// Job is one download. The file is written to Dest+".part" and renamed to
// Dest once it is complete and its checksum fits.
//
// A transfer that breaks off is continued from where it stopped (a ranged
// request), a few times in a row as long as it made progress. With
// KeepPartial the .part file also stays behind when the download fails or is
// cancelled, and the next Run with the same Dest continues from it. Both
// need a checksum: a file with Algo "none" always starts over.
type Job struct {
	URL, Dest   string
	Sum         Checksum
	Auth        *Auth
	KeepPartial bool
}

// errBadFile: the file arrived whole but is not the file wanted.
type errBadFile struct{ msg string }

func (e *errBadFile) Error() string { return e.msg }

func (j Job) Run(ctx context.Context) error {
	if j.Sum.Hex == "" && j.Sum.Algo != "none" {
		return errors.New("no checksum for " + j.URL)
	}
	part := j.Dest + ".part"
	resumable := j.Sum.Algo != "none"
	keep := j.KeepPartial && resumable
	if !keep {
		os.Remove(part)
	}
	err := j.run(ctx, part, resumable)
	if err != nil {
		var bad *errBadFile
		if !keep || errors.As(err, &bad) {
			os.Remove(part)
		}
		os.Remove(j.Dest)
		return err
	}
	os.Remove(j.Dest)
	if err := os.Rename(part, j.Dest); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

func (j Job) run(ctx context.Context, part string, resumable bool) error {
	report, _ := ctx.Value(progressKey{}).(func(done, total int64))
	var h hash.Hash = sha256.New()
	if strings.EqualFold(j.Sum.Algo, "md5") {
		h = md5.New()
	}
	out, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	// What an earlier run left: hash it and go on after it.
	have, _ := io.Copy(h, out)
	restart := func() error {
		h.Reset()
		have = 0
		if err := out.Truncate(0); err != nil {
			return err
		}
		_, err := out.Seek(0, io.SeekStart)
		return err
	}
	if have > 0 && (!resumable || (j.Sum.Size > 0 && have > j.Sum.Size)) {
		if err := restart(); err != nil {
			return err
		}
	}
	old := have > 0 // the file starts with bytes of an earlier run
	if old && j.Sum.Size > 0 && have == j.Sum.Size && j.verify(h, have) == nil {
		return out.Close() // it was complete already
	}
	if old {
		log.Printf("continuing the download of %s at %d MB", j.URL, have>>20)
	}

	// attempt asks for the file from `have` on and appends what arrives.
	attempt := func() (connected bool, err error) {
		actx, cancel := context.WithCancel(ctx)
		defer cancel()
		stall := time.AfterFunc(StallTimeout, cancel)
		defer stall.Stop()
		resp, err := get(actx, DownloadClient, j.URL, j.Auth, have)
		if err != nil {
			var se *StatusError
			if have > 0 && errors.As(err, &se) && se.Code == http.StatusRequestedRangeNotSatisfiable {
				// more bytes here than the server's file has: not this file
				if err := restart(); err != nil {
					return false, err
				}
				return true, io.ErrUnexpectedEOF
			}
			return false, err
		}
		defer resp.Body.Close()
		if have > 0 && resp.StatusCode == http.StatusOK {
			if err := restart(); err != nil { // no ranges here: the whole file again
				return true, err
			}
		}
		total := j.Sum.Size
		if total == 0 && resp.ContentLength > 0 {
			total = have + resp.ContentLength
		}
		if report != nil {
			report(have, total)
		}
		buf := make([]byte, 256<<10)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				stall.Reset(StallTimeout)
				if _, err := out.Write(buf[:n]); err != nil {
					return true, &errBadFile{err.Error()} // disk full and the like: no retry
				}
				h.Write(buf[:n])
				have += int64(n)
				if have > MaxDownload {
					return true, &errBadFile{fmt.Sprintf("%s is larger than %d GB", j.URL, MaxDownload>>30)}
				}
				if report != nil {
					report(have, total)
				}
			}
			if rerr == io.EOF {
				return true, nil
			}
			if rerr != nil {
				return true, rerr
			}
		}
	}

	fails := 0
	for {
		before := have
		connected, err := attempt()
		if err == nil {
			err = j.verify(h, have)
			if err == nil {
				return out.Close()
			}
			if !old {
				return err
			}
			// The bytes kept from an earlier run were of another file
			// (the mod was replaced since): once more from the start.
			log.Printf("%v; downloading it again from the start", err)
			old = false
			if err := restart(); err != nil {
				return err
			}
			continue
		}
		var bad *errBadFile
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !connected || errors.As(err, &bad) {
			if errors.Is(err, context.Canceled) {
				err = fmt.Errorf("%s: no answer for %s", j.URL, StallTimeout)
			}
			return err // get already tried again where that makes sense
		}
		if have > before {
			fails = 0
		}
		if fails >= len(RetryDelays) {
			return err
		}
		if errors.Is(err, context.Canceled) {
			err = fmt.Errorf("no data for %s", StallTimeout)
		}
		log.Printf("download of %s broke off at %d MB (%v); continuing in %s", j.URL, have>>20, err, RetryDelays[fails])
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(RetryDelays[fails]):
		}
		fails++
		if !resumable && have > 0 {
			if err := restart(); err != nil {
				return err
			}
		}
	}
}

func (j Job) verify(h hash.Hash, n int64) error {
	if j.Sum.Size > 0 && n != j.Sum.Size {
		return &errBadFile{fmt.Sprintf("download size %d, expected %d", n, j.Sum.Size)}
	}
	if j.Sum.Algo != "none" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, j.Sum.Hex) {
			return &errBadFile{fmt.Sprintf("checksum mismatch for %s (got %s, expected %s)", j.URL, got, j.Sum.Hex)}
		}
	}
	return nil
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
