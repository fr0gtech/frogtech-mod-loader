package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// A dev script copied a test build over the pak the loader wrote for UI Kit.
func TestForeignPakPutBackAndKeep(t *testing.T) {
	fakeCatalog(t)
	_, game := testutil.FakeLibrary(t)
	_, addons, _ := steam.Paths(game)
	a := testApp(t)
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallCatalogMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	pak := filepath.Join(addons, "pak01_dir.vpk")
	original, _ := os.ReadFile(pak)
	testBuild := testutil.BuildVPK(map[string][]byte{"panorama/layout/uikit/x.vxml_c": []byte("dev build")})
	os.WriteFile(pak, testBuild, 0o644)

	st := a.Status()
	if len(st.UnknownDetails) != 1 || st.UnknownPaks[0] != "pak01_dir.vpk" {
		t.Fatalf("details: %+v", st.UnknownDetails)
	}
	if r := st.UnknownDetails[0].Replaces; r == nil || r.Name != "UI Kit" || r.Version != "0.8.1" {
		t.Fatalf("replaces: %+v", r)
	}

	// A second, unrelated foreign file blocks a full apply; putting back
	// UI Kit must still restore its file.
	other := filepath.Join(addons, "pak07_dir.vpk")
	os.WriteFile(other, testutil.FakeVPK("someone else's"), 0o644)

	// Put back: the foreign file goes and the loader writes UI Kit again.
	if err := a.DeleteForeignFile("pak01_dir.vpk"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(pak); !bytes.Equal(got, original) {
		t.Fatal("UI Kit's file not put back")
	}
	if u := a.Status().UnknownPaks; len(u) != 1 || u[0] != "pak07_dir.vpk" {
		t.Fatalf("unknown after put back: %v", u)
	}
	if err := a.DeleteForeignFile("pak07_dir.vpk"); err != nil || len(a.Status().UnknownPaks) != 0 {
		t.Fatalf("deleting the other file: %v", err)
	}
	if err := a.DeleteForeignFile("pak01_dir.vpk"); err == nil {
		t.Fatal("deleted a file the loader wrote")
	}

	// Keep: the test build becomes a mod and UI Kit is turned off.
	os.WriteFile(pak, testBuild, 0o644)
	m, err := a.KeepForeign("pak01_dir.vpk")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "UI Kit (your copy)" || a.state.Mod("uikit").Enabled || !a.state.Mod(m.ID).Enabled {
		t.Fatalf("kept %+v; UI Kit enabled=%v", m, a.state.Mod("uikit").Enabled)
	}
	if got, _ := os.ReadFile(pak); !bytes.Equal(got, testBuild) || len(a.Status().UnknownPaks) != 0 {
		t.Fatal("kept build isn't what the game loads")
	}
}
