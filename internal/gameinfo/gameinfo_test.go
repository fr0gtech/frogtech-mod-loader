package gameinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// The block that boots with mods (verified in game on build 6785).
const patchedBlock = "\t\t\tGame_LowViolence\tcitadel_lv\r\n\r\n" +
	"\t\t\tGame\t\t\t\tcitadel/addons\r\n" +
	"\t\t\tGame\t\t\t\tcitadel\r\n" +
	"\t\t\tGame\t\t\t\tcore\r\n" +
	"\t\t\tMod\t\t\t\tcitadel\r\n" +
	"\t\t\tWrite\t\t\t\tcitadel\r\n" +
	"\t\t\tMod\t\t\t\tcore\r\n" +
	"\t\t\tWrite\t\t\t\tcore\r\n" +
	"\t\t}\r\n"

func TestEnsureStock(t *testing.T) {
	for _, name := range []string{"gameinfo_stock_6785.gi", "gameinfo_vanilla.gi"} {
		t.Run(name, func(t *testing.T) {
			in := testutil.Read(t, name)
			r, err := Check(in)
			if err != nil || r.Addons || len(r.MissingWrite) != 4 || r.Patched() {
				t.Fatalf("stock: %+v err=%v", r, err)
			}
			out, changed, err := Ensure(in)
			if err != nil || !changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if !strings.Contains(out, patchedBlock) {
				t.Fatalf("unexpected block:\n%q", block(out))
			}
			if len(out) != len(in)+len(patchedBlock)-len("\t\t\tGame_LowViolence\tcitadel_lv\r\n\r\n\t\t\tGame\t\t\t\tcitadel\r\n\t\t\tGame\t\t\t\tcore\r\n\t\t}\r\n") {
				t.Fatal("unexpected size change")
			}
			// CRLF preserved everywhere: no bare \n introduced.
			if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
				t.Fatal("line endings changed")
			}
			if r, _ := Check(out); !r.Patched() {
				t.Fatalf("not detected after patch: %+v", r)
			}
			// Idempotent.
			out2, changed, err := Ensure(out)
			if err != nil || changed || out2 != out {
				t.Fatalf("second run changed=%v err=%v", changed, err)
			}
			// Strip gives back Valve's file byte for byte.
			back, changed, err := Strip(out)
			if err != nil || !changed || back != in {
				t.Fatalf("strip: changed=%v err=%v\n%q", changed, err, block(back))
			}
			if _, changed, _ := Strip(in); changed {
				t.Fatal("stripping a stock file changed it")
			}
		})
	}
}

// A file patched by 0.6 and older (addons line only) must be fixed too: that
// is the file that stops the game from starting.
func TestEnsureAddonsOnly(t *testing.T) {
	stock := testutil.Read(t, "gameinfo_stock_6785.gi")
	old := strings.Replace(stock, "\t\t\tGame\t\t\t\tcitadel\r\n", "\t\t\tGame\t\t\t\tcitadel/addons\r\n\t\t\tGame\t\t\t\tcitadel\r\n", 1)
	r, err := Check(old)
	if err != nil || !r.Addons || r.Patched() || len(r.MissingWrite) != 4 {
		t.Fatalf("old patch: %+v err=%v", r, err)
	}
	out, changed, err := Ensure(old)
	if err != nil || !changed || !strings.Contains(out, patchedBlock) {
		t.Fatalf("changed=%v err=%v\n%q", changed, err, block(out))
	}
	if strings.Count(out, "citadel/addons") != 1 {
		t.Fatal("addons line duplicated")
	}
	if back, _, _ := Strip(out); back != stock {
		t.Fatal("strip didn't give the stock file back")
	}
}

func TestEnsureDMMUntouched(t *testing.T) {
	in := testutil.Read(t, "gameinfo_dmm.gi")
	if r, err := Check(in); err != nil || !r.Patched() {
		t.Fatalf("DMM file: %+v err=%v", r, err)
	}
	out, changed, err := Ensure(in)
	if err != nil || changed || out != in {
		t.Fatalf("DMM file should be left alone: changed=%v err=%v", changed, err)
	}
	// Turning mods off only takes the addons line out; DMM's own Mod/Write
	// lines (between its Game lines) stay.
	off, changed, err := Strip(in)
	if err != nil || !changed || strings.Contains(off, "citadel/addons") || strings.Count(off, "Write               c") != 2 {
		t.Fatalf("strip DMM: changed=%v err=%v\n%s", changed, err, block(off))
	}
	if len(off) != len(in)-len("            Game                citadel/addons\n") {
		t.Fatalf("strip removed more than the addons line:\n%s", block(off))
	}
}

func TestEnsureLF(t *testing.T) {
	in := "\"GameInfo\"\n{\n\tFileSystem\n\t{\n\t\tSearchPaths\n\t\t{\n\t\t\tGame_UILanguage\tcitadel_*LANGUAGE*\n\t\t\tMod  citadel\n\t\t\tGame  citadel\n\t\t}\n\t}\n}\n"
	out, changed, err := Ensure(in)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	want := "\t\t\tGame_UILanguage\tcitadel_*LANGUAGE*\n\t\t\tGame  citadel/addons\n\t\t\tMod  citadel\n\t\t\tGame  citadel\n" +
		"\t\t\tWrite  citadel\n\t\t\tMod  core\n\t\t\tWrite  core\n\t\t}\n"
	if !strings.Contains(out, want) {
		t.Fatalf("got %q", out)
	}
	if back, _, _ := Strip(out); back != in {
		t.Fatalf("strip: %q", back)
	}
}

func TestEnsureEmptyBlock(t *testing.T) {
	in := "SearchPaths\n{\n}\n"
	out, changed, err := Ensure(in)
	if err != nil || !changed || out != "SearchPaths\n{\n\tGame\t\t\t\tcitadel/addons\n\tMod\t\t\t\tcitadel\n\tWrite\t\t\t\tcitadel\n\tMod\t\t\t\tcore\n\tWrite\t\t\t\tcore\n}\n" {
		t.Fatalf("changed=%v err=%v %q", changed, err, out)
	}
}

func TestEnsureCommentedOut(t *testing.T) {
	in := "SearchPaths\n{\n\t// Game citadel/addons\n\tGame citadel\n}\n"
	out, changed, err := Ensure(in)
	if err != nil || !changed || !strings.Contains(out, "\tGame citadel/addons\n\tGame citadel\n\tMod citadel\n") {
		t.Fatalf("changed=%v err=%v out=%q", changed, err, out)
	}
}

func TestEnsureErrors(t *testing.T) {
	for _, in := range []string{"", "GameInfo { }", "SearchPaths\n{\n Game citadel\n"} {
		if _, _, err := Ensure(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestFixGameinfo(t *testing.T) {
	dir := t.TempDir()
	cit := filepath.Join(dir, "game", "citadel")
	os.MkdirAll(cit, 0o755)
	orig := testutil.Read(t, "gameinfo_stock_6785.gi")
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
	r, err := Status(dir)
	if err != nil || !r.Patched() {
		t.Fatal("status after fix", r, err)
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
	// Unfix restores the file Steam gave us.
	if changed, err := Unfix(dir); err != nil || !changed {
		t.Fatal("unfix", changed, err)
	}
	cur, _ := os.ReadFile(filepath.Join(cit, "gameinfo.gi"))
	if string(cur) != strings.Replace(orig, "citadel_lv", "citadel_lv2", 1) {
		t.Fatal("unfix didn't restore the stock file")
	}
	if r, _ := Status(dir); r.Addons {
		t.Fatal("addons still mounted after unfix")
	}
	if changed, _ := Unfix(dir); changed {
		t.Fatal("second unfix should be a no-op")
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
