package main

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
	"testing"
)

// TestAppFlow drives the App against a fake game folder and a fake catalog.
func TestAppFlow(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "uikit.zip")
	writeZip(t, zipPath, map[string][]byte{"pak01_dir.vpk": fakeVPK("uikit-v1"), "readme.txt": []byte("hi")})
	zipData, _ := os.ReadFile(zipPath)
	sum := sha256.Sum256(zipData)
	version := "0.8.0"
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"mods": []map[string]any{{
			"id": "uikit", "name": "UI Kit", "version": version, "url": srvURL + "/uikit.zip",
			"sha256": hex.EncodeToString(sum[:]), "size": len(zipData)}}})
	})
	mux.HandleFunc("/uikit.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(zipData) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	old := catalogURL
	catalogURL = srv.URL + "/catalog.json"
	defer func() { catalogURL = old }()

	_, game := fakeLibrary(t)
	_, addons, _ := gamePaths(game)
	a := testApp(t)
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.CheckUpdates(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallCatalogMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(addons, "pak01_dir.vpk"))
	if err != nil || !bytes.HasSuffix(b, []byte("uikit-v1")) {
		t.Fatalf("mod not applied: %v", err)
	}
	if ok, _ := GameinfoStatus(game); !ok {
		t.Fatal("gameinfo not patched on apply")
	}

	// A local upload goes after it.
	v := filepath.Join(t.TempDir(), "x.vpk")
	os.WriteFile(v, fakeVPK("local"), 0o644)
	if err := a.AddLocal(v, "My Skin_dir.vpk"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(addons, "pak02_dir.vpk")); err != nil {
		t.Fatal("local mod not applied")
	}

	// New catalog version -> auto-update installs and re-applies.
	writeZip(t, zipPath, map[string][]byte{"pak01_dir.vpk": fakeVPK("uikit-v2")})
	zipData, _ = os.ReadFile(zipPath)
	sum = sha256.Sum256(zipData)
	version = "0.9.0"
	if _, err := a.CheckUpdates(ctx, true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(addons, "pak01_dir.vpk"))
	if !bytes.HasSuffix(b, []byte("uikit-v2")) {
		t.Fatal("update not applied")
	}
	st := a.Status()
	if len(st.Mods) != 2 || st.Mods[0].Version != "0.9.0" || len(st.UnknownPaks) != 0 || !st.GameinfoOK {
		t.Fatalf("status: %+v", st)
	}

	// Disable + remove.
	id := st.Mods[1].ID
	if err := a.SetEnabled("uikit", false); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove(id); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(addons)
	for _, e := range ents {
		if IsPakName(e.Name()) {
			t.Fatalf("leftover %s", e.Name())
		}
	}
	if _, err := os.Stat(a.store.ModDir(id)); !os.IsNotExist(err) {
		t.Fatal("store dir not removed")
	}
}
