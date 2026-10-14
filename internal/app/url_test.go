package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// fakeCatalog serves a catalog with one mod ("uikit") and its zip.
func fakeCatalog(t *testing.T) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "uikit.zip")
	testutil.WriteZip(t, zipPath, map[string][]byte{"pak01_dir.vpk": testutil.FakeVPK("uikit")})
	zipData, _ := os.ReadFile(zipPath)
	sum := sha256.Sum256(zipData)
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"mods": []map[string]any{{
			"id": "uikit", "name": "UI Kit", "version": "0.8.1", "url": srvURL + "/uikit.zip", "image": srvURL + "/uikit.png",
			"sha256": hex.EncodeToString(sum[:])}}})
	})
	mux.HandleFunc("/uikit.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(zipData) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	old := catalog.FrogtechURL
	catalog.FrogtechURL = srv.URL + "/catalog.json"
	t.Cleanup(func() { catalog.FrogtechURL = old })
}

func lastToast(t *testing.T, a *App) Event {
	t.Helper()
	ev := a.TakeEvents()
	for i := len(ev) - 1; i >= 0; i-- {
		if ev[i].Kind == "toast" && ev[i].Level != "info" {
			return ev[i]
		}
	}
	t.Fatalf("no toast in %+v", ev)
	return Event{}
}

func TestHandleURL(t *testing.T) {
	fakeCatalog(t)
	_, game := testutil.FakeLibrary(t)
	_, addons, _ := steam.Paths(game)
	a := testApp(t)
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Install with no catalog loaded yet: the link loads it.
	a.HandleURL(ctx, "frogmod://install/uikit")
	if e := lastToast(t, a); e.Level != "ok" || e.Text != "UI Kit 0.8.1 installed." {
		t.Fatalf("toast %+v", e)
	}
	if _, err := os.Stat(filepath.Join(addons, "pak01_dir.vpk")); err != nil {
		t.Fatal("not applied")
	}
	if m := a.state.Mod("uikit"); m == nil || !m.Enabled || !strings.HasSuffix(m.Image, "/uikit.png") {
		t.Fatalf("mod %+v", m)
	}

	// Disabled + same Version: the link re-enables it.
	a.SetEnabled("uikit", false)
	a.HandleURL(ctx, "frogmod://install/uikit")
	if e := lastToast(t, a); e.Text != "UI Kit 0.8.1 enabled." {
		t.Fatalf("toast %+v", e)
	}

	a.HandleURL(ctx, "frogmod://install/nope")
	if e := lastToast(t, a); e.Level != "error" || !strings.Contains(e.Text, "not a Frogtech mod") {
		t.Fatalf("toast %+v", e)
	}
	a.HandleURL(ctx, "frogmod://explode")
	if e := lastToast(t, a); e.Level != "error" {
		t.Fatalf("toast %+v", e)
	}

	a.HandleURL(ctx, "frogmod://gamebanana/sound/83198")
	ev := a.TakeEvents()
	if len(ev) != 1 || ev[0].Kind != "open" || ev[0].Model != gamebanana.ModelSound || ev[0].GBID != 83198 {
		t.Fatalf("events %+v", ev)
	}
	if len(a.TakeEvents()) != 0 {
		t.Fatal("events not cleared")
	}
}

func TestDontAskSettings(t *testing.T) {
	a := testApp(t)
	if err := a.SetDontAsk("remove", true); err != nil {
		t.Fatal(err)
	}
	a.SetDontAsk("import", true)
	a.SetDontAsk("import", false)
	if err := a.SetDontAsk("", true); err == nil {
		t.Fatal("empty key accepted")
	}
	// Saving the other settings from the page must not drop the choices.
	s := a.state.Settings
	s.DontAsk = nil
	s.ShowMature = true
	if err := a.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	st, err := mods.LoadState(a.statePath)
	if err != nil || !st.Settings.DontAsk["remove"] || st.Settings.DontAsk["import"] || !st.Settings.ShowMature {
		t.Fatalf("%+v %v", st.Settings, err)
	}
	if err := a.ResetDontAsk(); err != nil {
		t.Fatal(err)
	}
	st, _ = mods.LoadState(a.statePath)
	if len(st.Settings.DontAsk) != 0 {
		t.Fatalf("not reset: %+v", st.Settings.DontAsk)
	}
}

func TestBulkAndForeign(t *testing.T) {
	_, game := testutil.FakeLibrary(t)
	_, addons, _ := steam.Paths(game)
	a := testApp(t)
	a.SetGameDir(game)
	os.MkdirAll(addons, 0o755)
	os.WriteFile(filepath.Join(addons, "pak01_dir.vpk"), testutil.FakeVPK("foreign"), 0o644)
	os.WriteFile(filepath.Join(addons, "notes.txt"), []byte("keep"), 0o644)
	for _, n := range []string{"a", "b", "c"} {
		p := filepath.Join(t.TempDir(), n+".vpk")
		os.WriteFile(p, testutil.FakeVPK(n), 0o644)
		if err := a.AddLocal(p, n+".vpk"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := a.DeleteForeign(); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := os.Stat(filepath.Join(addons, "notes.txt")); err != nil {
		t.Fatal("non-pak file touched")
	}
	ids := []string{a.state.Mods[0].ID, a.state.Mods[1].ID}
	if n, err := a.BulkMods(ids, "disable"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if a.state.Mods[0].Enabled || !a.state.Mods[2].Enabled || len(a.state.Applied) != 1 {
		t.Fatalf("after disable: %+v", a.state.Applied)
	}
	if n, err := a.BulkMods(append(ids, "missing"), "remove"); err != nil || n != 2 || len(a.state.Mods) != 1 {
		t.Fatal(n, err)
	}
	if _, err := a.BulkMods(ids, "explode"); err == nil {
		t.Fatal("bad action accepted")
	}
}

func TestImageHostAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://images.gamebanana.com/img/ss/mods/x.jpg": true,
		"https://mods.frogtech.dev/img/uikit.png":         true,
		"http://images.gamebanana.com/x.jpg":              false,
		"https://evil.example/x.png":                      false,
		"file:///C:/Windows/win.ini":                      false,
	} {
		u, _ := url.Parse(raw)
		if imageHostAllowed(u) != want {
			t.Errorf("%s: want %v", raw, want)
		}
	}
	a := testApp(t)
	if _, err := a.CachedImage(context.Background(), "https://evil.example/x.png"); err == nil {
		t.Fatal("proxied a foreign host")
	}
}
