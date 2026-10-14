package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetries(t *testing.T) {
	old := RetryDelays
	RetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { RetryDelays = old })
}

// A server that answers 502 twice (a redeploy) and then works: no error.
func TestRetriesTransientErrors(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	resp, err := Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok" || calls.Load() != 3 {
		t.Fatalf("body %q after %d calls", b, calls.Load())
	}
}

func TestNoRetryOnFinalErrors(t *testing.T) {
	fastRetries(t)
	for _, code := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
		}))
		_, err := Get(context.Background(), srv.URL)
		srv.Close()
		var se *StatusError
		if !errors.As(err, &se) || se.Code != code || calls.Load() != 1 {
			t.Errorf("HTTP %d: %v after %d calls", code, err, calls.Load())
		}
	}
}

func TestGivesUpAfterAllAttempts(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := Get(context.Background(), srv.URL)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 503 || int(calls.Load()) != len(RetryDelays)+1 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
	// A refused connection is retried too, then reported.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, err := Get(context.Background(), dead.URL); err == nil {
		t.Fatal("refused connection reported as success")
	}
}

func TestRetryAfter(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	var first time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if time.Since(first) < 900*time.Millisecond {
			t.Errorf("Retry-After not honoured: %s", time.Since(first))
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	resp, err := Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if r := retryAfterOf(&http.Response{Header: http.Header{"Retry-After": {"120"}}}); r != 120*time.Second {
		t.Fatal(r)
	}
}
