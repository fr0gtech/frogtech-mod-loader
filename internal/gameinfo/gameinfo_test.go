package gameinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

func TestEnsureAddonsLineVanilla(t *testing.T) {
	in := testutil.Read(t, "gameinfo_vanilla.gi")
	if ok, err := HasAddons(in); err != nil || ok {
		t.Fatalf("vanilla: has=%v err=%v", ok, err)
	}
	out, changed, err := EnsureAddonsLine(in)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "\t\t\tGame_LowViolence\tcitadel_lv\r\n\r\n\t\t\tGame\t\t\t\tcitadel/addons\r\n\t\t\tGame\t\t\t\tcitadel\r\n"
	if !strings.Contains(out, want) {
		t.Fatalf("addons line not inserted before Game citadel; got block:\n%q", block(out))
	}
	if len(out) != len(in)+len("\t\t\tGame\t\t\t\tcitadel/addons\r\n") {
		t.Fatalf("unexpected size change")
	}
	// CRLF preserved everywhere: no bare \n introduced.
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Fatal("line endings changed")
	}
	// Idempotent.
	out2, changed, err := EnsureAddonsLine(out)
	if err != nil || changed || out2 != out {
		t.Fatalf("second run changed=%v err=%v", changed, err)
	}
	if ok, _ := HasAddons(out); !ok {
		t.Fatal("not detected after insert")
	}
}

func TestEnsureAddonsLineDMMUntouched(t *testing.T) {
	in := testutil.Read(t, "gameinfo_dmm.gi")
	out, changed, err := EnsureAddonsLine(in)
	if err != nil || changed || out != in {
		t.Fatalf("DMM file should be left alone: changed=%v err=%v", changed, err)
	}
}

func TestEnsureAddonsLineLF(t *testing.T) {
	in := "\"GameInfo\"\n{\n\tFileSystem\n\t{\n\t\tSearchPaths\n\t\t{\n\t\t\tGame_UILanguage\tcitadel_*LANGUAGE*\n\t\t\tMod  citadel\n\t\t\tGame  citadel\n\t\t}\n\t}\n}\n"
	out, changed, err := EnsureAddonsLine(in)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	if !strings.Contains(out, "\t\t\tGame_UILanguage\tcitadel_*LANGUAGE*\n\t\t\tGame  citadel/addons\n\t\t\tMod  citadel\n") {
		t.Fatalf("got %q", out)
	}
}

func TestEnsureAddonsLineCommentedOut(t *testing.T) {
	in := "SearchPaths\n{\n\t// Game citadel/addons\n\tGame citadel\n}\n"
	out, changed, err := EnsureAddonsLine(in)
	if err != nil || !changed || !strings.Contains(out, "\tGame citadel/addons\n\tGame citadel\n") {
		t.Fatalf("changed=%v err=%v out=%q", changed, err, out)
	}
}

func TestEnsureAddonsLineErrors(t *testing.T) {
	for _, in := range []string{"", "GameInfo { }", "SearchPaths\n{\n Game citadel\n"} {
		if _, _, err := EnsureAddonsLine(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestFixGameinfo(t *testing.T) {
	dir := t.TempDir()
	cit := filepath.Join(dir, "game", "citadel")
	os.MkdirAll(cit, 0o755)
	orig := testutil.Read(t, "gameinfo_vanilla.gi")
	os.WriteFile(filepath.Join(cit, "gameinfo.gi"), []byte(orig), 0o644)

	changed, err := Fix(dir)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	bak, _ := os.ReadFile(filepath.Join(cit, "gameinfo.gi.bak"))
	if string(bak) != orig {
		t.Fatal("backup is not the original")
	}
	if st, err := os.Stat(filepath.Join(cit, "addons")); err != nil || !st.IsDir() {
		t.Fatal("addons dir not created")
	}
	ok, err := Status(dir)
	if err != nil || !ok {
		t.Fatal("status after fix", ok, err)
	}
	// Simulate a game update resetting the file: backup must not be overwritten.
	os.WriteFile(filepath.Join(cit, "gameinfo.gi"), []byte(strings.Replace(orig, "citadel_lv", "citadel_lv2", 1)), 0o644)
	if changed, err := Fix(dir); err != nil || !changed {
		t.Fatal(changed, err)
	}
	bak2, _ := os.ReadFile(filepath.Join(cit, "gameinfo.gi.bak"))
	if string(bak2) != orig {
		t.Fatal("backup overwritten")
	}
	if changed, _ := Fix(dir); changed {
		t.Fatal("third run should be a no-op")
	}
	ents, _ := os.ReadDir(cit)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatal("temp file left behind:", e.Name())
		}
	}
}

func block(s string) string {
	i := strings.Index(s, "SearchPaths")
	if i < 0 {
		return s
	}
	end := i + 600
	if end > len(s) {
		end = len(s)
	}
	return s[i:end]
}
