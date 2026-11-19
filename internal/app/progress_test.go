package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// slowCatalog serves "uikit" at the given version. While hold is set, the
// zip's download stops after its first half and waits.
type slowCatalog struct {
	mu      sync.Mutex
	version string
	hold    bool
	ranges  []string
}

func newSlowCatalog(t *testing.T) *slowCatalog {
	t.Helper()
	sc := &slowCatalog{version: "1.0.0", hold: true}
	zipPath := filepath.Join(t.TempDir(), "uikit.zip")
	testutil.WriteZip(t, zipPath, map[string][]byte{"pak01_dir.vpk": testutil.FakeVPK("uikit")})
	zipData, _ := os.ReadFile(zipPath)
	sum := sha256.Sum256(zipData)
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"mods": []map[string]any{{
			"id": "uikit", "name": "UI Kit", "version": sc.version, "url": srvURL + "/uikit.zip",
			"sha256": hex.EncodeToString(sum[:]), "size": len(zipData)}}})
	})
	mux.HandleFunc("/uikit.zip", func(w http.ResponseWriter, r *http.Request) {
		sc.mu.Lock()
		hold := sc.hold
		sc.ranges = append(sc.ranges, r.Header.Get("Range"))
		sc.mu.Unlock()
		if !hold {
			http.ServeContent(w, r, "uikit.zip", time.Time{}, bytes.NewReader(zipData))
			return
		}
		w.Header().Set("Content-Length", "999999")
		w.Write(zipData[:len(zipData)/2])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	old := catalog.FrogtechURL
	catalog.FrogtechURL = srv.URL + "/catalog.json"
	t.Cleanup(func() { catalog.FrogtechURL = old })
	return sc
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A running download shows its progress, can be stopped, keeps what it has,
// and the next install asks only for the rest.
func TestStopAndContinueDownload(t *testing.T) {
	sc := newSlowCatalog(t)
	a := testApp(t)
	if err := a.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Cancel() {
		t.Fatal("Cancel with nothing running said it stopped something")
	}
	errc := make(chan error, 1)
	go func() { errc <- a.InstallCatalogMod(context.Background(), "uikit") }()
	waitFor(t, "download progress", func() bool {
		p := a.Progress()
		return p.Phase == phaseDownload && p.Done > 0 && p.CanStop && p.Item == "UI Kit" && p.Busy != ""
	})
	if p := a.Progress(); p.Total <= p.Done {
		t.Fatalf("progress %d of %d", p.Done, p.Total)
	}
	if !a.Cancel() {
		t.Fatal("Cancel didn't stop the download")
	}
	if err := <-errc; !errors.Is(err, ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	if p := a.Progress(); p.Busy != "" || p.Phase != "" || p.CanStop {
		t.Fatalf("progress after stop: %+v", p)
	}
	if a.Status().Mods == nil || len(a.Status().Mods) != 0 {
		t.Fatal("a stopped install added a mod")
	}
	parts, _ := filepath.Glob(filepath.Join(a.DataDir(), "tmp", "*.part"))
	if len(parts) != 1 {
		t.Fatalf("kept parts: %v", parts)
	}
	fi, _ := os.Stat(parts[0])

	sc.mu.Lock()
	sc.hold = false
	sc.mu.Unlock()
	if err := a.InstallCatalogMod(context.Background(), "uikit"); err != nil {
		t.Fatal(err)
	}
	sc.mu.Lock()
	last := sc.ranges[len(sc.ranges)-1]
	sc.mu.Unlock()
	if want := "bytes=" + itoa(fi.Size()) + "-"; last != want {
		t.Fatalf("the second install asked %q, want %q", last, want)
	}
	if m := a.Status().Mods; len(m) != 1 || m[0].ID != "uikit" {
		t.Fatalf("mods after install: %+v", m)
	}
	if left, _ := os.ReadDir(filepath.Join(a.DataDir(), "tmp")); len(left) != 0 {
		t.Fatalf("temp folder not empty: %v", left)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// Old leftovers in the temp folder go at start; a recent unfinished download stays.
func TestCleanTemp(t *testing.T) {
	a := testApp(t)
	tmp := filepath.Join(a.DataDir(), "tmp")
	os.MkdirAll(tmp, 0o755)
	keep, oldPart, junk := filepath.Join(tmp, "dl-a.download.part"), filepath.Join(tmp, "dl-b.download.part"), filepath.Join(tmp, "gb-1.download")
	for _, p := range []string{keep, oldPart, junk} {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	long := time.Now().Add(-partialMaxAge - time.Hour)
	os.Chtimes(oldPart, long, long)
	a.cleanTemp()
	left, _ := os.ReadDir(tmp)
	if len(left) != 1 || left[0].Name() != filepath.Base(keep) {
		t.Fatalf("left: %v", left)
	}
}

// One mod can be checked on its own.
func TestCheckMod(t *testing.T) {
	sc := newSlowCatalog(t)
	sc.hold = false
	a := testApp(t)
	ctx := context.Background()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallCatalogMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	if res, err := a.CheckMod(ctx, "uikit"); err != nil || res.Update != "" || res.Note != "" {
		t.Fatalf("current mod: %+v, %v", res, err)
	}
	sc.mu.Lock()
	sc.version = "1.1.0"
	sc.mu.Unlock()
	res, err := a.CheckMod(ctx, "uikit")
	if err != nil || res.Update != "UI Kit 1.1.0" {
		t.Fatalf("after a release: %+v, %v", res, err)
	}
	if m := a.Status().Mods[0]; m.Update != "UI Kit 1.1.0" {
		t.Fatalf("status doesn't show the update: %q", m.Update)
	}

	vpk := filepath.Join(t.TempDir(), "mine.vpk")
	os.WriteFile(vpk, testutil.FakeVPK("mine"), 0o644)
	if err := a.AddLocal(vpk, "mine.vpk"); err != nil {
		t.Fatal(err)
	}
	var local string
	for _, m := range a.Status().Mods {
		if m.Source == "local" {
			local = m.ID
		}
	}
	if res, err := a.CheckMod(ctx, local); err != nil || res.Update != "" || !strings.Contains(res.Note, "added yourself") {
		t.Fatalf("own file: %+v, %v", res, err)
	}
	if _, err := a.CheckMod(ctx, "nope"); err == nil {
		t.Fatal("unknown mod: no error")
	}
}
