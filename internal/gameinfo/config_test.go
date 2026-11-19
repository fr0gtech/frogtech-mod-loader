package gameinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

const cfgText = `// a performance config
"GameInfo"
{
	game "other"
	FileSystem
	{
		SearchPaths
		{
			Game citadel/custom
		}
	}
	SceneSystem
	{
		"VolumetricFog" "0"
		NewKey 5
	}
	ConVars
	{
		"some_new_cvar" "1" // comment
	}
	Brand
	{
		New
		{
			a "b c"
		}
	}
}
`

func TestOverlay(t *testing.T) {
	base := "GameInfo\r\n{\r\n\tgame \t\"citadel\"\r\n\tFileSystem\r\n\t{\r\n\t\tSearchPaths\r\n\t\t{\r\n\t\t\tGame citadel\r\n\t\t}\r\n\t}\r\n" +
		"\tSceneSystem\r\n\t{\r\n\t\tVolumetricFog 1 // keep\r\n\t\tOther 2\r\n\t}\r\n\tConVars\r\n\t{\r\n\t\told 1\r\n\t}\r\n}\r\n"
	got, err := Overlay(base, cfgText)
	if err != nil {
		t.Fatal(err)
	}
	want := "GameInfo\r\n{\r\n\tgame \t\"other\"\r\n\tFileSystem\r\n\t{\r\n\t\tSearchPaths\r\n\t\t{\r\n\t\t\tGame citadel\r\n\t\t}\r\n\t}\r\n" +
		"\tSceneSystem\r\n\t{\r\n\t\tVolumetricFog \"0\" // keep\r\n\t\tOther 2\r\n\t\tNewKey\t5\r\n\t}\r\n\tConVars\r\n\t{\r\n\t\told 1\r\n\t\t\"some_new_cvar\"\t\"1\"\r\n\t}\r\n" +
		"\tBrand\r\n\t{\r\n\t\tNew\r\n\t\t{\r\n\t\t\ta\t\"b c\"\r\n\t\t}\r\n\t}\r\n}\r\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	// merging again changes nothing
	if again, _ := Overlay(got, cfgText); again != got {
		t.Fatal("a second merge changed the file")
	}
	for _, bad := range []string{"", "GameInfo {", "Other { a 1 }", "GameInfo { a }"} {
		if CheckConfig(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// A config mod on Valve's file: merged, kept across runs, and taken out
// again byte for byte.
func TestFixWithConfig(t *testing.T) {
	dir := t.TempDir()
	cit := filepath.Join(dir, "game", "citadel")
	os.MkdirAll(cit, 0o755)
	orig := testutil.Read(t, "gameinfo_stock_6785.gi")
	path := filepath.Join(cit, "gameinfo.gi")
	os.WriteFile(path, []byte(orig), 0o644)
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	cfg := Config{Name: "Perf", Text: cfgText}

	// on a file the loader already patched
	if changed, err := Fix(dir); err != nil || !changed {
		t.Fatal(changed, err)
	}
	patched := read()
	if changed, err := Fix(dir, cfg); err != nil || !changed {
		t.Fatal(changed, err)
	}
	got := read()
	if !Merged(got) || !strings.Contains(got, "some_new_cvar") || strings.Contains(got, "citadel/custom") {
		t.Fatalf("not merged as expected:\n%s", got[:300])
	}
	if r, err := Status(dir); err != nil || !r.Patched() {
		t.Fatal("status", r, err)
	}
	if b, _ := os.ReadFile(path + BaseSuffix); string(b) != orig {
		t.Fatal("the kept base is not Valve's file")
	}
	if changed, _ := Fix(dir, cfg); changed {
		t.Fatal("second run should be a no-op")
	}
	// another config replaces the first, from Valve's file
	other := Config{Name: "Other", Text: "GameInfo { ConVars { second_cvar 1 } }"}
	if changed, err := Fix(dir, other); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if got := read(); strings.Contains(got, "some_new_cvar") || !strings.Contains(got, "second_cvar") {
		t.Fatal("the first config is still in, or the second is not")
	}
	// both: the first in the list wins
	a := Config{Name: "A", Text: "GameInfo { ConVars { x 1 } }"}
	b := Config{Name: "B", Text: "GameInfo { ConVars { x 2 y 3 } }"}
	Fix(dir, a, b)
	if got := read(); !strings.Contains(got, "x\t1") || !strings.Contains(got, "y\t3") || !strings.Contains(got, "from: A, B") {
		t.Fatalf("order: %s", got[:200])
	}
	// switched off: the patched file without the config
	if changed, err := Fix(dir); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if read() != patched {
		t.Fatal("the config is not fully out")
	}
	if _, err := os.Stat(path + BaseSuffix); err == nil {
		t.Fatal("base left behind")
	}
	// mods off with a config in: Valve's file
	Fix(dir, cfg)
	if changed, err := Unfix(dir); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if read() != orig {
		t.Fatal("unfix didn't give Valve's file back")
	}
	// a game update replaces a merged file: the new file is the base
	Fix(dir, cfg)
	updated := strings.Replace(orig, "citadel_lv", "citadel_lv2", 1)
	os.WriteFile(path, []byte(updated), 0o644)
	Fix(dir, cfg)
	if b, _ := os.ReadFile(path + BaseSuffix); string(b) != updated {
		t.Fatal("the base is not the updated file")
	}
	// a config that can't be read is reported, the rest still works
	if _, err := Fix(dir, Config{Name: "Bad", Text: "nope"}); err == nil {
		t.Fatal("no error for a bad config")
	}
	if r, _ := Status(dir); !r.Patched() {
		t.Fatal("not patched after a bad config")
	}
}
