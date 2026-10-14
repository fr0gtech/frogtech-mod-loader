package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// repoServer serves a repository and its files, recording Authorization.
type repoServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth map[string]string // path -> Authorization header seen
	doc  func() any
	key  string // required on /repo.json and /dl/* when set
}

func newRepoServer(t *testing.T, key string) *repoServer {
	rs := &repoServer{auth: map[string]string{}, key: key}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.auth[r.URL.Path] = r.Header.Get("Authorization")
		rs.mu.Unlock()
		needKey := rs.key != "" && (r.URL.Path == "/repo.json" || strings.HasPrefix(r.URL.Path, "/dl/"))
		if needKey && r.Header.Get("Authorization") != "Bearer "+rs.key {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/repo.json":
			json.NewEncoder(w).Encode(rs.doc())
		case strings.HasPrefix(r.URL.Path, "/redirect/"):
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
		default:
			if data, ok := files.Load(r.URL.Path); ok {
				w.Write(data.([]byte))
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

var files sync.Map // path -> zip bytes, shared by the test servers

func zipFor(t *testing.T, path, tag string) (string, int) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.zip")
	testutil.WriteZip(t, p, map[string][]byte{"pak01_dir.vpk": testutil.FakeVPK(tag)})
	b, _ := os.ReadFile(p)
	files.Store(path, b)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), len(b)
}

func (rs *repoServer) seen(path string) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.auth[path]
}

func repoMod(id, name, version, url, sum string, size int) map[string]any {
	return map[string]any{"id": id, "name": name, "version": version, "url": url, "sha256": sum, "size": size}
}

// The key goes to the repository's own origin only: never to another port,
// not even through a redirect.
func TestRepoKeyOriginScoping(t *testing.T) {
	a := testApp(t)
	A := newRepoServer(t, "k-secret-1234")
	B := newRepoServer(t, "")
	sumA, nA := zipFor(t, "/dl/a1.zip", "a1")
	sumB, nB := zipFor(t, "/b1.zip", "b1")
	sumC, nC := zipFor(t, "/b2.zip", "b2")
	A.doc = func() any {
		return map[string]any{"format": 1, "name": "Paid Mods", "mods": []any{
			repoMod("a1", "A One", "1.0", A.srv.URL+"/dl/a1.zip", sumA, nA),
			repoMod("b1", "B One", "1.0", B.srv.URL+"/b1.zip", sumB, nB),
			repoMod("b2", "B Two", "1.0", A.srv.URL+"/redirect/x?to="+url.QueryEscape(B.srv.URL+"/b2.zip"), sumC, nC),
		}}
	}
	ctx := context.Background()
	if _, err := a.AddRepo(ctx, A.srv.URL+"/repo.json", ""); err == nil || !strings.Contains(err.Error(), "needs a key") {
		t.Fatalf("no key: %v", err)
	}
	if _, err := a.AddRepo(ctx, A.srv.URL+"/repo.json", "wrong"); err == nil || !strings.Contains(err.Error(), "key not accepted") {
		t.Fatalf("wrong key: %v", err)
	}
	r, err := a.AddRepo(ctx, A.srv.URL+"/repo.json", "k-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "b1", "b2"} {
		if err := a.InstallRepoMod(ctx, r.ID, id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if A.seen("/repo.json") != "Bearer k-secret-1234" || A.seen("/dl/a1.zip") != "Bearer k-secret-1234" {
		t.Fatalf("same-origin requests lacked the key: %q %q", A.seen("/repo.json"), A.seen("/dl/a1.zip"))
	}
	if B.seen("/b1.zip") != "" || B.seen("/b2.zip") != "" {
		t.Fatalf("key leaked to another origin: %q %q", B.seen("/b1.zip"), B.seen("/b2.zip"))
	}
	if m := a.state.Mod(r.ID + "/b2"); m == nil || m.Source != mods.SourceRepo || m.RepoMod != "b2" {
		t.Fatalf("namespaced install: %+v", m)
	}
	data, _ := os.ReadFile(a.statePath)
	if strings.Contains(string(data), "k-secret-1234") || !strings.Contains(string(data), `"keyLast4": "1234"`) {
		t.Fatal("state.json must hold only the protected key and its last 4")
	}
	// Removing the key: the repository now says so.
	if err := a.SetRepoKey(ctx, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if st := a.repoStates[r.ID]; st.status != catalog.StatusKeyRejected {
		t.Fatalf("status %q", st.status)
	}
}

// Repository mods install under "<repoId>/<modId>" and go into the game.
func TestRepoInstallApplies(t *testing.T) {
	_, game := testutil.FakeLibrary(t)
	_, addons, _ := steam.Paths(game)
	a := testApp(t)
	a.SetGameDir(game)
	S := newRepoServer(t, "")
	sum, n := zipFor(t, "/cool.zip", "cool")
	S.doc = func() any {
		return map[string]any{"format": 1, "name": "Example Mods", "mods": []any{repoMod("cool_hud", "Cool HUD", "1.2.0", S.srv.URL+"/cool.zip", sum, n)}}
	}
	r, err := a.AddRepo(context.Background(), S.srv.URL+"/repo.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.InstallRepoMod(context.Background(), r.ID, "cool_hud"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.store.ModDir(r.ID+"/cool_hud"), "pak01_dir.vpk")); err != nil {
		t.Fatal("store layout:", err)
	}
	if b, _ := os.ReadFile(filepath.Join(addons, "pak01_dir.vpk")); !strings.HasSuffix(string(b), "cool") {
		t.Fatal("not applied")
	}
	// Reserved / duplicate.
	if _, err := a.AddRepo(context.Background(), S.srv.URL+"/repo.json", ""); err == nil {
		t.Fatal("duplicate repository accepted")
	}
}

// One broken repository never blocks updates from the others.
func TestFailingRepoDoesNotBlockOthers(t *testing.T) {
	fakeCatalog(t)
	_, game := testutil.FakeLibrary(t)
	a := testApp(t)
	a.SetGameDir(game)
	a.state.Settings.AutoUpdate = true
	good := newRepoServer(t, "")
	version := "1.0.0"
	sum1, n1 := zipFor(t, "/g1.zip", "g1")
	sum2, n2 := zipFor(t, "/g2.zip", "g2")
	good.doc = func() any {
		sum, n := sum1, n1
		if version != "1.0.0" {
			sum, n = sum2, n2
		}
		return map[string]any{"format": 1, "name": "Good", "mods": []any{repoMod("g", "Good Mod", version, good.srv.URL+fmt.Sprintf("/g%d.zip", map[bool]int{true: 1, false: 2}[version == "1.0.0"]), sum, n)}}
	}
	flaky := newRepoServer(t, "")
	broken := false
	flaky.doc = func() any { return map[string]any{"format": 1, "name": "Flaky", "mods": []any{}} }
	ctx := context.Background()
	rg, err := a.AddRepo(ctx, good.srv.URL+"/repo.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddRepo(ctx, flaky.srv.URL+"/repo.json", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallRepoMod(ctx, rg.ID, "g"); err != nil {
		t.Fatal(err)
	}
	// Now Flaky breaks and Good publishes 1.1.0.
	flaky.srv.Close()
	broken = true
	version = "1.1.0"
	res, err := a.CheckUpdates(ctx, true)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !broken || len(res.RepoErrors) != 1 || !strings.HasPrefix(res.RepoErrors[0], "Flaky:") {
		t.Fatalf("repo errors: %v", res.RepoErrors)
	}
	if len(res.Installed) != 1 || res.Installed[0] != "Good Mod 1.1.0" {
		t.Fatalf("installed: %v", res.Installed)
	}
	if m := a.state.Mod(rg.ID + "/g"); m.Version != "1.1.0" {
		t.Fatalf("version %s", m.Version)
	}
}
