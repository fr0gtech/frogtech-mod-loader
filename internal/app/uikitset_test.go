package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

var (
	keyA = strings.Repeat("a", 64)
	keyB = strings.Repeat("b", 64)
	keyC = strings.Repeat("c", 64)
)

type rebuildCall struct{ Key, Game, Core string }

// setSite fakes mods.frogtech.dev's theme set API: GET <key>.vpk and
// POST <key>/rebuild. By default a rebuild answers 200 with the same key.
type setSite struct {
	mu     sync.Mutex
	vpks   map[string][]byte
	answer func(c rebuildCall) (int, any) // called with mu held
	calls  []rebuildCall
}

func newSetSite() *setSite { return &setSite{vpks: map[string][]byte{}} }

func (s *setSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/uikit/sets/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := strings.CutSuffix(rest, ".vpk"); ok && r.Method == http.MethodGet {
		b, ok := s.vpks[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
		return
	}
	key, ok := strings.CutSuffix(rest, "/rebuild")
	if !ok || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var body struct{ Game, Core string }
	json.NewDecoder(r.Body).Decode(&body)
	c := rebuildCall{key, body.Game, body.Core}
	s.calls = append(s.calls, c)
	code, out := http.StatusOK, any(map[string]string{"key": key})
	if s.answer != nil {
		code, out = s.answer(c)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(out)
}

func (s *setSite) put(key string, b []byte) { s.mu.Lock(); s.vpks[key] = b; s.mu.Unlock() }

func (s *setSite) setAnswer(f func(c rebuildCall) (int, any)) {
	s.mu.Lock()
	s.answer = f
	s.mu.Unlock()
}

func (s *setSite) callList() []rebuildCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]rebuildCall(nil), s.calls...)
}

// newKeyFor answers a rebuild for game with key (other games: 404).
func newKeyFor(game, key string) func(c rebuildCall) (int, any) {
	return func(c rebuildCall) (int, any) {
		if c.Game != game {
			return http.StatusNotFound, map[string]string{"error": "game not served yet"}
		}
		return http.StatusOK, map[string]string{"key": key}
	}
}

func storeSet(t *testing.T, a *App) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.store.ModDir("uikit"), "uikit_set_dir.vpk"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// withSet: buildsSetup, and the player's set A installed on game 6821.
func withSet(t *testing.T) (s *buildsServer, dataDir, game, addons string, a *App) {
	t.Helper()
	s, dataDir, game, addons = buildsSetup(t)
	s.site.put(keyA, testutil.FakeVPK("set A"))
	a = restart(t, dataDir)
	if err := a.InstallSet(context.Background(), keyA); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set A")) {
		t.Fatal("setup: set A not applied")
	}
	return s, dataDir, game, addons, a
}

// "Test in game" with UI Kit not installed: UI Kit from the catalog first, then the set.
func TestInstallSetInstallsUIKitFirst(t *testing.T) {
	newFakeGame(t)
	s := newBuildsServer(t)
	s.listBuild("6821", "1.0.0", s.zips["uikit_1.0.0_g6821.zip"], true, "")
	s.site.put(keyA, testutil.FakeVPK("set A"))
	_, game := testutil.FakeLibrary(t)
	setClientVersion(t, game, "6821")
	a := restart(t, t.TempDir())
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallSet(context.Background(), keyA); err != nil {
		t.Fatal(err)
	}
	m := a.state.Mod("uikit")
	if m == nil || !m.Enabled || m.Set != keyA || m.SetGame != "6821" || m.SetCore != "1.0.0" || m.SetNote != "" {
		t.Fatalf("uikit %+v", m)
	}
	_, addons, _ := steam.Paths(game)
	if !bytes.Equal(storeSet(t, a), testutil.FakeVPK("set A")) ||
		!bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6821")) ||
		!bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set A")) {
		t.Fatal("set not in place of the starter set")
	}
	// The set is built for this game and core before it is downloaded.
	if c := s.site.callList(); len(c) != 1 || c[0] != (rebuildCall{keyA, "6821", "1.0.0"}) {
		t.Fatalf("rebuild calls %+v", c)
	}
}

func TestInstallSetBadSignature(t *testing.T) {
	s, dataDir, _, addons := buildsSetup(t)
	s.site.put(keyA, []byte("<html>not a set</html>"))
	a := restart(t, dataDir)
	if err := a.InstallSet(context.Background(), keyA); err == nil {
		t.Fatal("no error for a file that is not a VPK")
	}
	if !bytes.Equal(storeSet(t, a), testutil.FakeVPK("set 6821")) ||
		!bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6821")) {
		t.Fatal("the starter set was replaced")
	}
	if m := a.state.Mod("uikit"); m.Set != "" || m.SetGame != "" {
		t.Fatalf("recorded a set that isn't installed: %+v", m)
	}
}

func TestInstallSetBadKey(t *testing.T) {
	_, dataDir, _, _ := buildsSetup(t)
	a := restart(t, dataDir)
	for _, k := range []string{"", keyA[:63], strings.ToUpper(keyA), "../" + keyA[3:]} {
		if err := a.InstallSet(context.Background(), k); err == nil {
			t.Fatalf("%q accepted", k)
		}
	}
}

// Game update: parked first, then the build for the new game (starter set),
// then the set rebuilt for the new game and core.
func TestSetRebuiltAfterGameUpdate(t *testing.T) {
	s, dataDir, game, addons, _ := withSet(t)
	before := len(s.site.callList())
	setClientVersion(t, game, "6822")
	a := restart(t, dataDir)
	ctx := context.Background()
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	assertParked(t, a, addons)
	if len(s.site.callList()) != before {
		t.Fatal("asked for a rebuild while UI Kit is parked")
	}
	s.listBuild("6822", "1.0.1", uikitZip(t, "6822"), true, "")
	s.site.put(keyB, testutil.FakeVPK("set B"))
	s.site.setAnswer(newKeyFor("6822", keyB))
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	c := s.site.callList()
	if len(c) != before+1 || c[len(c)-1] != (rebuildCall{keyA, "6822", "1.0.1"}) {
		t.Fatalf("rebuild calls %+v", c[before:])
	}
	m := a.state.Mod("uikit")
	if m.Parked || m.Game != "6822" || m.Set != keyB || m.SetGame != "6822" || m.SetCore != "1.0.1" {
		t.Fatalf("uikit %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak01_dir.vpk"), testutil.FakeVPK("core 6822")) ||
		!bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set B")) {
		t.Fatal("paks after the rebuild")
	}
	if a.waitingForBuilds() {
		t.Fatal("still waiting after the rebuild")
	}
	// Nothing more to do.
	if err := a.checkGameBuilds(ctx); err != nil || len(s.site.callList()) != before+1 {
		t.Fatalf("asked again: %v", err)
	}
}

// The site can't build for the new game yet (or disagrees on the core, or
// can't be reached): the starter set runs and the loader asks again on the
// parked retry cadence.
func TestSetRebuildLater(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(c rebuildCall) (int, any)
	}{
		{"404", func(rebuildCall) (int, any) {
			return http.StatusNotFound, map[string]string{"error": "game not served yet"}
		}},
		{"409", func(rebuildCall) (int, any) {
			return http.StatusConflict, map[string]string{"error": "the loader and the site disagree on the UI Kit core", "core": "1.0.0"}
		}},
		{"network", func(rebuildCall) (int, any) { panic(http.ErrAbortHandler) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dataDir, game, addons, _ := withSet(t)
			setClientVersion(t, game, "6822")
			s.listBuild("6822", "1.0.1", uikitZip(t, "6822"), true, "")
			s.site.setAnswer(tc.answer)
			a := restart(t, dataDir)
			ctx := context.Background()
			before := len(s.site.callList())
			a.checkGameBuilds(ctx)
			m := a.state.Mod("uikit")
			if m.Parked || m.Game != "6822" || m.Set != keyA || m.SetGame != "" || m.SetNote != "" {
				t.Fatalf("uikit %+v", m)
			}
			if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6822")) {
				t.Fatal("the starter set for game 6822 is not in place")
			}
			if !a.waitingForBuilds() {
				t.Fatal("not retried on the parked cadence")
			}
			for _, v := range a.Status().Mods {
				if v.ID == "uikit" && !strings.Contains(v.Themes, "built for game 6822") {
					t.Fatalf("page message %q", v.Themes)
				}
			}
			a.checkGameBuilds(ctx)
			if n := len(s.site.callList()) - before; n != 2 {
				t.Fatalf("%d rebuild calls, want 2", n)
			}
			s.site.put(keyB, testutil.FakeVPK("set B"))
			s.site.setAnswer(newKeyFor("6822", keyB))
			if err := a.checkGameBuilds(ctx); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set B")) || a.waitingForBuilds() {
				t.Fatal("set B not installed once the site could build it")
			}
		})
	}
}

// The themes don't build for the new game: the page says so, the starter
// set stays, and the site is not asked again for that game.
func TestSetRebuildProblems(t *testing.T) {
	s, dataDir, game, addons, _ := withSet(t)
	setClientVersion(t, game, "6822")
	s.listBuild("6822", "1.0.1", uikitZip(t, "6822"), true, "")
	s.site.setAnswer(func(rebuildCall) (int, any) {
		return http.StatusUnprocessableEntity, map[string]any{"problems": []map[string]any{
			{"file": "themes/mine/layout/hud.xml", "line": 3, "message": "unknown panel"}}}
	})
	a := restart(t, dataDir)
	ctx := context.Background()
	before := len(s.site.callList())
	for i := 0; i < 3; i++ {
		a.checkGameBuilds(ctx)
	}
	if n := len(s.site.callList()) - before; n != 1 {
		t.Fatalf("%d rebuild calls for the same game, want 1", n)
	}
	const note = "Your themes don't build for game 6822: fix them on mods.frogtech.dev/themes"
	if m := a.state.Mod("uikit"); m.SetNote != note || m.Set != keyA {
		t.Fatalf("uikit %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6822")) {
		t.Fatal("the starter set is not in place")
	}
	if a.waitingForBuilds() {
		t.Fatal("still retrying after the site refused the themes")
	}
	for _, m := range a.Status().Mods {
		if m.ID == "uikit" && m.Themes != note {
			t.Fatalf("page message %q", m.Themes)
		}
	}
}

// A UI Kit release on the same game: the set is rebuilt for the new core.
func TestSetRebuiltOnCoreChange(t *testing.T) {
	s, _, _, addons, a := withSet(t)
	ctx := context.Background()
	s.mu.Lock()
	s.uikitVersion = "1.0.1"
	s.mu.Unlock()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	s.site.put(keyC, testutil.FakeVPK("set C"))
	s.site.setAnswer(func(c rebuildCall) (int, any) {
		if c.Core != "1.0.1" {
			return http.StatusConflict, map[string]string{"error": "core", "core": "1.0.1"}
		}
		return http.StatusOK, map[string]string{"key": keyC}
	})
	if err := a.UpdateMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	m := a.state.Mod("uikit")
	if m.Version != "1.0.1" || m.Set != keyA || m.SetGame != "" {
		t.Fatalf("uikit after the update %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6821")) {
		t.Fatal("the update's starter set is not in place")
	}
	if err := a.checkGameBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	if c := s.site.callList(); c[len(c)-1] != (rebuildCall{keyA, "6821", "1.0.1"}) {
		t.Fatalf("rebuild calls %+v", c)
	}
	if m := a.state.Mod("uikit"); m.Set != keyC || m.SetCore != "1.0.1" ||
		!bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set C")) {
		t.Fatalf("set C not installed: %+v", m)
	}
}

// Installing UI Kit by hand again, or removing it, forgets the set.
func TestManualInstallClearsSet(t *testing.T) {
	s, _, _, addons, a := withSet(t)
	ctx := context.Background()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallCatalogMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	if m := a.state.Mod("uikit"); m.Set != "" || m.SetGame != "" {
		t.Fatalf("uikit %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6821")) {
		t.Fatal("not the starter set")
	}
	before := len(s.site.callList())
	a.checkGameBuilds(ctx)
	if len(s.site.callList()) != before {
		t.Fatal("rebuilt a forgotten set")
	}
	a.state.Mod("uikit").Set = keyA
	if err := a.Remove("uikit"); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallCatalogMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	if m := a.state.Mod("uikit"); m.Set != "" {
		t.Fatalf("set kept through a remove: %+v", m)
	}
}

// The link while Deadlock runs: the set goes in when the game is closed.
func TestSetLinkWhileGameRunning(t *testing.T) {
	s, dataDir, _, addons := buildsSetup(t)
	f := newFakeGame(t)
	f.set(true)
	s.site.put(keyA, testutil.FakeVPK("set A"))
	a := restart(t, dataDir)
	a.TakeEvents()
	a.HandleURL(context.Background(), "frogmod://uikit-set/"+keyA)
	ev := a.TakeEvents()
	if len(ev) == 0 || ev[len(ev)-1].Level != "ok" || !strings.Contains(ev[len(ev)-1].Text, "Restart Deadlock to see your themes") {
		t.Fatalf("events %+v", ev)
	}
	if !a.state.PendingApply || !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6821")) {
		t.Fatal("wrote the game folder while the game runs")
	}
	f.set(false)
	var seen string
	a.gameinfoTick(&seen)
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set A")) {
		t.Fatal("set A not applied once the game closed")
	}
}

// The site can't build the set for the player's game yet when the link is
// opened: nothing from another game goes in, the set is installed later.
func TestSetLinkGameNotServed(t *testing.T) {
	s, dataDir, _, addons := buildsSetup(t)
	s.site.put(keyA, testutil.FakeVPK("set A built for 6822"))
	s.site.setAnswer(newKeyFor("6822", keyA))
	a := restart(t, dataDir)
	a.TakeEvents()
	a.HandleURL(context.Background(), "frogmod://uikit-set/"+keyA)
	ev := a.TakeEvents()
	if len(ev) == 0 || ev[len(ev)-1].Level != "info" {
		t.Fatalf("events %+v", ev)
	}
	m := a.state.Mod("uikit")
	if m.Set != keyA || m.SetGame != "" || !a.waitingForBuilds() {
		t.Fatalf("uikit %+v", m)
	}
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set 6821")) {
		t.Fatal("installed a set built for another game")
	}
	s.site.put(keyB, testutil.FakeVPK("set B"))
	s.site.setAnswer(newKeyFor("6821", keyB))
	if err := a.checkGameBuilds(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readPak(t, addons, "pak02_dir.vpk"), testutil.FakeVPK("set B")) {
		t.Fatal("set not installed once the site could build it")
	}
}
