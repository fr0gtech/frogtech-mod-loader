package steam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

func TestParseVDF(t *testing.T) {
	v, err := ParseVDF(testutil.Read(t, "appmanifest_1422450.acf"))
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
	libs := parseLibraryFolders(testutil.Read(t, "libraryfolders.vdf"))
	if len(libs) != 1 || libs[0] != `C:\Program Files (x86)\Steam` {
		t.Fatalf("libs = %v", libs)
	}
	multi := `"libraryfolders" { "0" { "path" "C:\\Steam" } "1" { "path" "D:\\SteamLibrary" "apps" { "1422450" "1" } } "contentstatsid" "123" }`
	if libs := parseLibraryFolders(multi); len(libs) != 2 {
		t.Fatalf("multi libs = %v", libs)
	}
}

func TestReadGameInfo(t *testing.T) {
	_, game := testutil.FakeLibrary(t)
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
