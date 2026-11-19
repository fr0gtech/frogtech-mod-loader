package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/gameinfo"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// A config mod (an archive with only a gameinfo.gi) installs: its settings
// are in the game's file while it is on and out again when it is off or
// removed.
func TestConfigMod(t *testing.T) {
	newFakeGame(t)
	_, game := testutil.FakeLibrary(t)
	_, _, gi := steam.Paths(game)
	a := testApp(t)
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(gi); return string(b) }
	if _, err := a.EnsureGameinfo(); err != nil {
		t.Fatal(err)
	}
	plain := read()

	src := filepath.Join(t.TempDir(), "perf.zip")
	testutil.WriteZip(t, src, map[string][]byte{"gameinfo.gi": []byte("GameInfo\n{\n\tConVars\n\t{\n\t\tfrog_test_cvar 1\n\t}\n}\n")})
	if err := a.AddLocal(src, "Perf Config.zip"); err != nil {
		t.Fatal(err)
	}
	st := a.Status()
	if len(st.Mods) != 1 || st.Mods[0].Config == "" || len(st.Mods[0].Files) != 0 || !st.Mods[0].Enabled {
		t.Fatalf("mods: %+v", st.Mods)
	}
	id := st.Mods[0].ID
	got := read()
	if !gameinfo.Merged(got) || !strings.Contains(got, "frog_test_cvar") || !strings.Contains(got, "Perf Config") {
		t.Fatal("the config is not in the game's gameinfo.gi")
	}
	if r, _ := gameinfo.Status(game); !r.Patched() {
		t.Fatal("mods are not on with the config in")
	}
	if err := a.SetEnabled(id, false); err != nil {
		t.Fatal(err)
	}
	if read() != plain {
		t.Fatal("switched off, the config is still in")
	}
	if err := a.SetEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), "frog_test_cvar") {
		t.Fatal("switched on again, the config is not in")
	}
	if err := a.Remove(id); err != nil {
		t.Fatal(err)
	}
	if read() != plain {
		t.Fatal("removed, the config is still in")
	}
}

// The player's own values for a config mod's variables win over the mod's.
func TestConfigValues(t *testing.T) {
	newFakeGame(t)
	_, game := testutil.FakeLibrary(t)
	_, _, gi := steam.Paths(game)
	a := testApp(t)
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(gi); return string(b) }
	src := filepath.Join(t.TempDir(), "perf.zip")
	testutil.WriteZip(t, src, map[string][]byte{"gameinfo.gi": []byte("GameInfo\n{\n\tConVars\n\t{\n\t\t// --- View ---\n\t\tfrog_far \"6800\" // default: \"-1\" | how far you see\n\t\t// frog_solo \"true\" // default: \"false\" | solo only\n\t}\n}\n")})
	if err := a.AddLocal(src, "Perf.zip"); err != nil {
		t.Fatal(err)
	}
	id := a.Status().Mods[0].ID
	es, err := a.ConfigEntries(id)
	if err != nil || len(es) != 2 || es[0].Key != "frog_far" || es[0].Default != "-1" || es[0].Note != "how far you see" || es[0].Section != "View" || !es[1].Off || es[0].Set != nil {
		t.Fatalf("entries %+v %v", es, err)
	}
	if got := read(); !strings.Contains(got, "frog_far\t\"6800\"") || strings.Contains(got, "frog_solo") {
		t.Fatalf("before: %s", got[len(got)-200:])
	}
	v1, v2 := "9000", "true"
	if err := a.SetConfigValues(id, map[string]*string{"frog_far": &v1, "frog_solo": &v2}); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "frog_far\t\"9000\"") || !strings.Contains(got, "\"frog_solo\"\t\"true\"") || strings.Contains(got, "6800") {
		t.Fatalf("after: %s", got[len(got)-200:])
	}
	if es, _ := a.ConfigEntries(id); es[0].Set == nil || *es[0].Set != "9000" {
		t.Fatalf("set not reported: %+v", es[0])
	}
	// kept across a restart
	if b := restart(t, a.DataDir()); b.state.Mod(id).ConfigSet["frog_far"] != "9000" {
		t.Fatal("values not saved")
	}
	// back to the mod's own
	if err := a.SetConfigValues(id, map[string]*string{"frog_far": nil, "frog_solo": nil}); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "frog_far\t\"6800\"") || strings.Contains(got, "frog_solo") {
		t.Fatalf("undone: %s", got[len(got)-200:])
	}
	bad := "a\"b"
	if a.SetConfigValues(id, map[string]*string{"frog_far": &bad}) == nil || a.SetConfigValues(id, map[string]*string{"nope": &v1}) == nil {
		t.Fatal("a bad value or an unknown key was accepted")
	}
}
