package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testFile(n int) ([]byte, Checksum) {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	sum := sha256.Sum256(data)
	return data, Checksum{Algo: "sha256", Hex: hex.EncodeToString(sum[:]), Size: int64(n)}
}

// rangeServer serves data with Range support. cut(call) > 0 makes that
// request break off after so many bytes of its reply.
func rangeServer(t *testing.T, data []byte, ranges bool, cut func(call int) int) (*httptest.Server, *atomic.Int32, *[]string) {
	var calls atomic.Int32
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(calls.Add(1))
		rng := r.Header.Get("Range")
		seen = append(seen, rng)
		from := 0
		if ranges && rng != "" {
			from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
			if from >= len(data) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(data)-1, len(data)))
			w.Header().Set("Content-Length", strconv.Itoa(len(data)-from))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		}
		body := data[from:]
		if n := cut(call); n > 0 && n < len(body) {
			w.Write(body[:n])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // drops the connection mid-file
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &seen
}

func noCut(int) int { return 0 }

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: %d bytes, not the %d served", path, len(got), len(want))
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		t.Fatalf("%s.part left behind", path)
	}
}

// A transfer that breaks off twice goes on from where it stopped.
func TestDownloadContinuesAfterBreak(t *testing.T) {
	fastRetries(t)
	data, sum := testFile(600_000)
	srv, calls, seen := rangeServer(t, data, true, func(call int) int {
		if call <= 2 {
			return 200_000
		}
		return 0
	})
	dest := filepath.Join(t.TempDir(), "f")
	var last, total int64
	ctx := WithProgress(context.Background(), func(d, tot int64) {
		if d < last {
			t.Errorf("progress went back: %d after %d", d, last)
		}
		last, total = d, tot
	})
	if err := Download(ctx, srv.URL, dest, sum); err != nil {
		t.Fatal(err)
	}
	checkFile(t, dest, data)
	if calls.Load() != 3 || (*seen)[1] != "bytes=200000-" || (*seen)[2] != "bytes=400000-" {
		t.Fatalf("requests: %q", *seen)
	}
	if last != 600_000 || total != 600_000 {
		t.Fatalf("last progress %d of %d", last, total)
	}
}

// A server without ranges: the file is fetched again from the start.
func TestDownloadWithoutRanges(t *testing.T) {
	fastRetries(t)
	data, sum := testFile(300_000)
	srv, calls, _ := rangeServer(t, data, false, func(call int) int {
		if call == 1 {
			return 100_000
		}
		return 0
	})
	dest := filepath.Join(t.TempDir(), "f")
	if err := Download(context.Background(), srv.URL, dest, sum); err != nil {
		t.Fatal(err)
	}
	checkFile(t, dest, data)
	if calls.Load() != 2 {
		t.Fatalf("%d calls", calls.Load())
	}
}

// Cancelled with KeepPartial: the part stays and the next run asks only for
// the rest.
func TestDownloadResumesKeptPartial(t *testing.T) {
	fastRetries(t)
	data, sum := testFile(500_000)
	srv, _, seen := rangeServer(t, data, true, noCut)
	dest := filepath.Join(t.TempDir(), "f")
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithProgress(ctx, func(d, _ int64) {
		if d >= 100_000 {
			cancel()
		}
	})
	err := Job{URL: srv.URL, Dest: dest, Sum: sum, KeepPartial: true}.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want cancelled", err)
	}
	fi, err := os.Stat(dest + ".part")
	if err != nil || fi.Size() == 0 || fi.Size() >= 500_000 {
		t.Fatalf("part after cancel: %v %v", fi, err)
	}
	kept := fi.Size()
	if err := (Job{URL: srv.URL, Dest: dest, Sum: sum, KeepPartial: true}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkFile(t, dest, data)
	if want := fmt.Sprintf("bytes=%d-", kept); (*seen)[len(*seen)-1] != want {
		t.Fatalf("second run asked %q, want %q", (*seen)[len(*seen)-1], want)
	}
}

// Without KeepPartial a cancel leaves nothing.
func TestDownloadCancelLeavesNothing(t *testing.T) {
	data, sum := testFile(500_000)
	srv, _, _ := rangeServer(t, data, true, noCut)
	dest := filepath.Join(t.TempDir(), "f")
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithProgress(ctx, func(d, _ int64) { cancel() })
	if err := Download(ctx, srv.URL, dest, sum); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left behind", p)
		}
	}
}

// A kept part of another file (the mod's file was replaced): the checksum
// fails once, then the file is fetched whole.
func TestDownloadStalePartial(t *testing.T) {
	fastRetries(t)
	data, sum := testFile(300_000)
	srv, calls, _ := rangeServer(t, data, true, noCut)
	dest := filepath.Join(t.TempDir(), "f")
	os.WriteFile(dest+".part", bytes.Repeat([]byte("x"), 1000), 0o644)
	if err := (Job{URL: srv.URL, Dest: dest, Sum: sum, KeepPartial: true}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkFile(t, dest, data)
	if calls.Load() != 2 {
		t.Fatalf("%d calls", calls.Load())
	}
}

// A wrong file is an error and nothing stays, kept part or not.
func TestDownloadChecksumMismatch(t *testing.T) {
	data, sum := testFile(1000)
	sum.Hex = strings.Repeat("0", 64)
	srv, _, _ := rangeServer(t, data, true, noCut)
	dest := filepath.Join(t.TempDir(), "f")
	err := Job{URL: srv.URL, Dest: dest, Sum: sum, KeepPartial: true}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left behind", p)
		}
	}
}

// A transfer that stops moving is cut off and continued.
func TestDownloadStall(t *testing.T) {
	fastRetries(t)
	old := StallTimeout
	StallTimeout = 150 * time.Millisecond
	t.Cleanup(func() { StallTimeout = old })
	data, sum := testFile(200_000)
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data[:50_000])
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-"))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[from:])
	}))
	defer srv.Close()
	defer close(release)
	dest := filepath.Join(t.TempDir(), "f")
	if err := Download(context.Background(), srv.URL, dest, sum); err != nil {
		t.Fatal(err)
	}
	checkFile(t, dest, data)
}

// It gives up when the transfer keeps breaking without getting further.
func TestDownloadGivesUp(t *testing.T) {
	fastRetries(t)
	data, sum := testFile(100_000)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f")
	if err := Download(context.Background(), srv.URL, dest, sum); err == nil {
		t.Fatal("no error")
	}
	if calls.Load() != int32(len(RetryDelays))+1 {
		t.Fatalf("%d calls", calls.Load())
	}
}
