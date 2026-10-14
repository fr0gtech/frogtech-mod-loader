package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVDF(t *testing.T) {
	v, err := ParseVDF(readTestdata(t, "appmanifest_1422450.acf"))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Get("AppState", "installdir"); got != "Deadlock" {
		t.Errorf("installdir = %q", got)
	}
	if got := v.Get("appstate", "BUILDID"); got != "25917577" {
		t.Errorf("buildid = %q", got)
	}
	if got := v.Get("AppState", "LauncherPath"); got != `C:\Program Files (x86)\Steam\steam.exe` {
		t.Errorf("escape handling: %q", got)
	}
	if _, err := ParseVDF(`"a" { "b" "c"`); err == nil {
		t.Error("expected error for unclosed block")
	}
	v, err = ParseVDF("// comment\nroot { key value \"q\" \"x\\\"y\" }")
	if err != nil || v.Get("root", "key") != "value" || v.Get("root", "q") != `x"y` {
		t.Errorf("bare tokens/escapes: %v %v", v, err)
	}
}

func TestParseLibraryFolders(t *testing.T) {
	libs := parseLibraryFolders(readTestdata(t, "libraryfolders.vdf"))
	if len(libs) != 1 || libs[0] != `C:\Program Files (x86)\Steam` {
		t.Fatalf("libs = %v", libs)
	}
	multi := `"libraryfolders" { "0" { "path" "C:\\Steam" } "1" { "path" "D:\\SteamLibrary" "apps" { "1422450" "1" } } "contentstatsid" "123" }`
	if libs := parseLibraryFolders(multi); len(libs) != 2 {
		t.Fatalf("multi libs = %v", libs)
	}
}

// fakeLibrary builds <lib>/steamapps/{appmanifest,common/Deadlock/game/citadel}.
func fakeLibrary(t *testing.T) (lib, game string) {
	lib = t.TempDir()
	sa := filepath.Join(lib, "steamapps")
	game = filepath.Join(sa, "common", "Deadlock")
	cit := filepath.Join(game, "game", "citadel")
	os.MkdirAll(cit, 0o755)
	os.WriteFile(filepath.Join(sa, "appmanifest_1422450.acf"), []byte(readTestdata(t, "appmanifest_1422450.acf")), 0o644)
	os.WriteFile(filepath.Join(cit, "steam.inf"), []byte(readTestdata(t, "steam.inf")), 0o644)
	os.WriteFile(filepath.Join(cit, "gameinfo.gi"), []byte(readTestdata(t, "gameinfo_vanilla.gi")), 0o644)
	return lib, game
}

func TestReadGameInfo(t *testing.T) {
	_, game := fakeLibrary(t)
	gi := ReadGameInfo(game)
	if gi.BuildID != "25917577" || gi.ClientVersion != "6779" {
		t.Fatalf("%+v", gi)
	}
	for _, sub := range []string{"", "game", "game/citadel", "game/citadel/addons"} {
		os.MkdirAll(filepath.Join(game, "game", "citadel", "addons"), 0o755)
		if got := NormalizeGameDir(filepath.Join(game, sub)); got != game {
			t.Errorf("NormalizeGameDir(%q) = %q", sub, got)
		}
	}
	if NormalizeGameDir(t.TempDir()) != "" {
		t.Error("random dir accepted")
	}
}
