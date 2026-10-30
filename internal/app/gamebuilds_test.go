package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// buildsServer serves a catalog with UI Kit (two VPKs, a builds.json) and a
// second mod without builds, plus UI Kit zips for other game builds.
type buildsServer struct {
	url           string
	mu            sync.Mutex
	builds        map[string]any // builds.json "builds"
	zips          map[string][]byte
	noBuildsField bool
	buildsHits    int    // builds.json requests
	onCatalog     func() // called on each catalog.json request
	uikitGame     string
}

func zipBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.zip")
	testutil.WriteZip(t, p, files)
	b, _ := os.ReadFile(p)
	return b
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func uikitZip(t *testing.T, tag string) []byte {
	return zipBytes(t, map[string][]byte{
		"uikit_core_dir.vpk": testutil.FakeVPK("core " + tag),
		"uikit_set_dir.vpk":  testutil.FakeVPK("set " + tag),
		"readme.txt":         []byte("UI Kit"),
	})
}

func newBuildsServer(t *testing.T) *buildsServer {
	t.Helper()
	s := &buildsServer{builds: map[string]any{}, zips: map[string][]byte{}, uikitGame: "6821"}
	s.zips["uikit_1.0.0_g6821.zip"] = uikitZip(t, "6821")
	other := zipBytes(t, map[string][]byte{"other_dir.vpk": testutil.FakeVPK("other")})
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		hook := s.onCatalog
		s.mu.Unlock()
		if hook != nil {
			hook()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		uk := map[string]any{"id": "uikit", "name": "UI Kit", "version": "1.0.0", "game": s.uikitGame,
			"url": s.url + "/map-data/uikit/uikit_1.0.0_g6821.zip", "sha256": sha(s.zips["uikit_1.0.0_g6821.zip"])}
		if !s.noBuildsField {
			uk["builds"] = s.url + "/map-data/uikit/builds.json"
		}
		json.NewEncoder(w).Encode(map[string]any{"mods": []map[string]any{uk,
			{"id": "other", "name": "Other", "version": "1.0.0", "game": "6821", "url": s.url + "/other.zip", "sha256": sha(other)}}})
	})
	mux.HandleFunc("/map-data/uikit/builds.json", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.buildsHits++
		json.NewEncoder(w).Encode(map[string]any{"format": 1, "builds": s.builds})
	})
	mux.HandleFunc("/map-data/uikit/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		b, ok := s.zips[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	})
	mux.HandleFunc("/other.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(other) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	old := catalog.FrogtechURL
	catalog.FrogtechURL = srv.URL + "/catalog.json"
	t.Cleanup(func() { catalog.FrogtechURL = old })
	return s
}

// listBuild puts an entry for game ver into builds.json (zip served when given).
func (s *buildsServer) listBuild(ver, version string, zip []byte, published bool, shaOverride string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file := "uikit_" + version + "_g" + ver + ".zip"
	if zip != nil {
		s.zips[file] = zip
	}
	sum := shaOverride
	if sum == "" {
		sum = sha(zip)
	}
	s.builds[ver] = map[string]any{"version": version, "file": file, "sha256": sum, "size": len(zip), "published": published}
}

func setClientVersion(t *testing.T, game, ver string) {
	t.Helper()
	cit, _, _ := steam.Paths(game)
	if err := os.WriteFile(filepath.Join(cit, "steam.inf"), []byte("ClientVersion="+ver+"\nProductName=citadel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readPak(t *testing.T, addons, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(addons, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildsSetup installs UI Kit (pak01, pak02) and Other (pak03) on game 6821.
func buildsSetup(t *testing.T) (s *buildsServer, dataDir, game, addons string) {
	t.Helper()
	newFakeGame(t)
	s = newBuildsServer(t)
	s.listBuild("6821", "1.0.0", s.zips["uikit_1.0.0_g6821.zip"], true, "")
	_, game = testutil.FakeLibrary(t)
	setClientVersion(t, game, "6821")
	_, addons, _ = steam.Paths(game)
	dataDir = t.TempDir()
	a, err := NewApp(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"uikit", "other"} {
		if err := a.InstallCatalogMod(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if m := a.state.Mod("uikit"); m == nil || m.Game != "6821" || m.Parked {
		t.Fatalf("uikit after install: %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6821")) ||
		!bytes.Equal(readPak(t, addons, "pak03_dir.vpk"), testutil.FakeVPK("other")) {
		t.Fatal("setup paks wrong")
	}
	return s, dataDir, game, addons
}

// restart is a new loader process on the same data folder (no catalog yet).
func restart(t *testing.T, dataDir string) *App {
	t.Helper()
	a, err := NewApp(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func assertParked(t *testing.T, a *App, addons string) {
	t.Helper()
	ph := mods.PlaceholderVPK()
	for _, p := range []string{"pak01_dir.vpk", "pak02_dir.vpk"} {
		if !bytes.Equal(readPak(t, addons, p), ph) {
			t.Fatalf("%s is not the placeholder", p)
		}
	}
	if !bytes.Equal(readPak(t, addons, "pak03_dir.vpk"), testutil.FakeVPK("other")) {
		t.Fatal("the other mod's pak changed")
	}
	if m := a.state.Mod("uikit"); !m.Parked || m.Game != "6821" {
		t.Fatalf("uikit %+v", m)
	}
}

func TestParkAtStart(t *testing.T) {
	_, dataDir, game, addons := buildsSetup(t)
	setClientVersion(t, game, "6822") // Steam updated the game
	a := restart(t, dataDir)
	if err := a.checkGameBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertParked(t, a, addons)
	if u, _ := mods.UnknownPaks(addons, a.state.Applied); len(u) != 0 {
		t.Fatalf("placeholders seen as foreign: %v", u)
	}
	st := a.Status()
	for _, m := range st.Mods {
		want := ""
		if m.ID == "uikit" {
			want = "waiting for a build for game 6822"
		}
		if m.Waiting != want {
			t.Fatalf("%s waiting %q, want %q", m.ID, m.Waiting, want)
		}
	}
}

func TestParkedUntilBuildListed(t *testing.T) {
	s, dataDir, game, addons := buildsSetup(t)
	setClientVersion(t, game, "6822")
	a := restart(t, dataDir)
	ctx := context.Background()
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	assertParked(t, a, addons)

	// The catalog still offers the 6821 build as a newer version: not for this game.
	s.mu.Lock()
	s.zips["uikit_1.0.0_g6821.zip"] = uikitZip(t, "6821 again")
	s.mu.Unlock()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	a.state.Mod("uikit").Version = "0.9.0"
	for _, u := range a.pendingUpdates() {
		if u.modID == "uikit" {
			t.Fatalf("offered the catalog build for game 6821 on 6822: %s", u.label)
		}
	}
	a.state.Mod("uikit").Version = "1.0.0"

	zip := uikitZip(t, "6822")
	s.listBuild("6822", "1.0.1", zip, false, "") // built, not published yet
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	assertParked(t, a, addons)

	s.listBuild("6822", "1.0.1", zip, true, "")
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	m := a.state.Mod("uikit")
	if m.Parked || m.Game != "6822" || m.Version != "1.0.1" || !m.Enabled || m.Order != 1 {
		t.Fatalf("uikit after the build: %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6822")) ||
		!bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6822")) ||
		!bytes.Equal(readPak(t, addons, "pak03_dir.vpk"), testutil.FakeVPK("other")) {
		t.Fatal("paks after the build")
	}
	// Saved: a restart keeps it.
	if m := restart(t, dataDir).state.Mod("uikit"); m.Parked || m.Game != "6822" {
		t.Fatalf("state after restart: %+v", m)
	}
}

func TestBadSHAStaysParked(t *testing.T) {
	s, dataDir, game, addons := buildsSetup(t)
	setClientVersion(t, game, "6822")
	s.listBuild("6822", "1.0.1", uikitZip(t, "6822"), true, sha([]byte("something else")))
	a := restart(t, dataDir)
	if err := a.checkGameBuilds(context.Background()); err == nil {
		t.Fatal("no error for a bad checksum")
	}
	assertParked(t, a, addons)
	if m := a.state.Mod("uikit"); m.Version != "1.0.0" {
		t.Fatalf("version %s", m.Version)
	}
}

func TestNoBuildsFieldUnchanged(t *testing.T) {
	newFakeGame(t)
	s := newBuildsServer(t)
	s.noBuildsField = true
	_, game := testutil.FakeLibrary(t)
	setClientVersion(t, game, "6821")
	_, addons, _ := steam.Paths(game)
	dataDir := t.TempDir()
	a := restart(t, dataDir)
	a.SetGameDir(game)
	ctx := context.Background()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"uikit", "other"} {
		if err := a.InstallCatalogMod(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	before := [][]byte{readPak(t, addons, "pak01_dir.vpk"), readPak(t, addons, "pak02_dir.vpk"), readPak(t, addons, "pak03_dir.vpk")}
	setClientVersion(t, game, "6822")
	a = restart(t, dataDir)
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{"pak01_dir.vpk", "pak02_dir.vpk", "pak03_dir.vpk"} {
		if !bytes.Equal(readPak(t, addons, p), before[i]) {
			t.Fatalf("%s changed", p)
		}
	}
	for _, m := range a.state.Mods {
		if m.Parked {
			t.Fatalf("%s parked without builds", m.ID)
		}
	}
}

// A UI Kit installed before the loader recorded the game build is not parked.
func TestUnknownGameNotParked(t *testing.T) {
	_, dataDir, game, addons := buildsSetup(t)
	a := restart(t, dataDir)
	a.state.Mod("uikit").Game = ""
	a.saveLocked()
	setClientVersion(t, game, "6822")
	a = restart(t, dataDir)
	if err := a.checkGameBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m := a.state.Mod("uikit"); m.Parked {
		t.Fatal("parked a mod with no recorded game build")
	}
	if !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6821")) {
		t.Fatal("pak changed")
	}
}

// The game goes back to the build the files were made for: unparked.
func TestUnparkWhenGameMatchesAgain(t *testing.T) {
	_, dataDir, game, addons := buildsSetup(t)
	setClientVersion(t, game, "6822")
	a := restart(t, dataDir)
	ctx := context.Background()
	a.checkGameBuilds(ctx)
	assertParked(t, a, addons)
	setClientVersion(t, game, "6821")
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	if a.state.Mod("uikit").Parked || !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6821")) {
		t.Fatal("not unparked")
	}
}

func (s *buildsServer) hits() int { s.mu.Lock(); defer s.mu.Unlock(); return s.buildsHits }

// Steam updates the game while the loader runs: the watcher parks the stale
// mod before it puts the addons line back (no hourly check needed).
func TestParkOnGameUpdateWhileRunning(t *testing.T) {
	s, dataDir, game, addons := buildsSetup(t)
	a := restart(t, dataDir)
	var seen string
	a.gameinfoTick(&seen)
	if seen != "6821" || a.state.Mod("uikit").Parked {
		t.Fatalf("first round: seen %q, parked %v", seen, a.state.Mod("uikit").Parked)
	}
	// The update: new steam.inf, Valve's gameinfo.gi.
	setClientVersion(t, game, "6822")
	_, _, gi := steam.Paths(game)
	os.WriteFile(gi, []byte(testutil.Read(t, "gameinfo_stock_6785.gi")), 0o644)

	// Another operation holds opMu: gameinfo.gi stays Valve's this round.
	a.opMu.Lock()
	a.gameinfoTick(&seen)
	a.opMu.Unlock()
	if st := a.Status(); st.Gameinfo.Addons {
		t.Fatal("addons line back before the mod was parked")
	}
	if !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6821")) {
		t.Fatal("pak changed while busy")
	}

	a.gameinfoTick(&seen)
	assertParked(t, a, addons)
	if st := a.Status(); !st.Gameinfo.Patched() {
		t.Fatal("gameinfo.gi not patched after parking")
	}
	select {
	case <-a.checkNow:
	default:
		t.Fatal("no check asked for the builds")
	}
	if seen != "6822" || s.hits() != 0 {
		t.Fatalf("seen %q, builds fetched %d times by the watcher", seen, s.hits())
	}
}

// Frogtech repository switched off: parked (the game starts), no download.
func TestFrogtechDisabledParksWithoutDownload(t *testing.T) {
	s, dataDir, game, addons := buildsSetup(t)
	s.listBuild("6822", "1.0.1", uikitZip(t, "6822"), true, "")
	setClientVersion(t, game, "6822")
	a := restart(t, dataDir)
	a.state.FrogtechDisabled = true
	before := s.hits()
	if err := a.checkGameBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertParked(t, a, addons)
	if m := a.state.Mod("uikit"); m.Version != "1.0.0" || s.hits() != before {
		t.Fatalf("downloaded with Frogtech off: version %s, builds hits %d", m.Version, s.hits()-before)
	}
}

// A switched-off mod is recorded as parked but gets no build download.
func TestDisabledModNoDownload(t *testing.T) {
	s, dataDir, game, addons := buildsSetup(t)
	s.listBuild("6822", "1.0.1", uikitZip(t, "6822"), true, "")
	setClientVersion(t, game, "6822")
	a := restart(t, dataDir)
	a.state.Mod("uikit").Enabled = false
	before := s.hits()
	if err := a.checkGameBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := a.state.Mod("uikit")
	if !m.Parked || m.Version != "1.0.0" || s.hits() != before {
		t.Fatalf("parked %v, version %s, builds hits %d", m.Parked, m.Version, s.hits()-before)
	}
	if !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("other")) {
		t.Fatal("the other mod should be pak01 with UI Kit off")
	}
}

// At start the update loop parks before its first catalog request.
func TestStartParksBeforeCatalog(t *testing.T) {
	s, dataDir, game, addons := buildsSetup(t)
	setClientVersion(t, game, "6822")
	a := restart(t, dataDir)
	got := make(chan bool, 1)
	s.mu.Lock()
	s.onCatalog = func() {
		b, _ := os.ReadFile(filepath.Join(addons, "pak01_dir.vpk"))
		select {
		case got <- bytes.Equal(b, mods.PlaceholderVPK()):
		default:
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { a.updateLoop(); close(done) }()
	select {
	case parked := <-got:
		if !parked {
			t.Error("catalog requested before the stale mod was parked")
		}
	case <-time.After(10 * time.Second):
		t.Error("no catalog request")
	}
	a.Quit()
	<-done
}
