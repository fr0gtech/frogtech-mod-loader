package mods

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

func TestExtractVPKs(t *testing.T) {
	dir := t.TempDir()
	z := filepath.Join(dir, "m.zip")
	testutil.WriteZip(t, z, map[string][]byte{
		"readme.txt":               []byte("hi"),
		"mod/pak01_dir.vpk":        testutil.FakeVPK("a"),
		"optional/pak01_dir.vpk":   testutil.FakeVPK("b"),
		"extra/../../evil_dir.vpk": testutil.FakeVPK("c"),
	})
	out := filepath.Join(dir, "out")
	names, err := ExtractVPKs(z, out, "x.zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("names = %v", names)
	}
	for _, n := range names {
		if filepath.Dir(filepath.Join(out, n)) != out {
			t.Fatalf("escaped: %s", n)
		}
	}
	// bare vpk
	v := filepath.Join(dir, "upload.bin")
	os.WriteFile(v, testutil.FakeVPK("z"), 0o644)
	names, err = ExtractVPKs(v, filepath.Join(dir, "o2"), "My Mod_dir.vpk")
	if err != nil || len(names) != 1 || names[0] != "My Mod_dir.vpk" {
		t.Fatalf("%v %v", names, err)
	}
	// 7z (testdata/mod.7z made with py7zr: My Mod/pak01_dir.vpk + readme)
	names, err = ExtractVPKs(filepath.Join(testutil.Dir(), "mod.7z"), filepath.Join(dir, "o3"), "mod.7z")
	if err != nil || len(names) != 1 || names[0] != "pak01_dir.vpk" {
		t.Fatalf("7z: %v %v", names, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "o3", "pak01_dir.vpk")); !bytes.HasSuffix(b, []byte("seven")) {
		t.Fatal("7z content wrong")
	}
	// broken 7z / rar are errors, not crashes
	for _, magic := range []string{"7z\xbc\xaf\x27\x1c\x00\x04", "Rar!\x1a\x07\x01\x00"} {
		p := filepath.Join(dir, "broken.bin")
		os.WriteFile(p, []byte(magic), 0o644)
		if _, err := ExtractVPKs(p, filepath.Join(dir, "o3b"), "x"); err == nil {
			t.Fatalf("broken archive %q accepted", magic)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "o3b")); len(ents) != 0 {
		t.Fatal("broken archive left files")
	}
	// zip with fake vpk
	bad := filepath.Join(dir, "bad.zip")
	testutil.WriteZip(t, bad, map[string][]byte{"pak01_dir.vpk": []byte("not a vpk")})
	if _, err := ExtractVPKs(bad, filepath.Join(dir, "o4"), ""); err == nil {
		t.Fatal("invalid VPK accepted")
	}
	// zip without vpk
	none := filepath.Join(dir, "none.zip")
	testutil.WriteZip(t, none, map[string][]byte{"inner.rar": []byte("Rar!")})
	if _, err := ExtractVPKs(none, filepath.Join(dir, "o5"), ""); err == nil {
		t.Fatal("expected error for zip without vpk")
	}
}

func TestIsPakName(t *testing.T) {
	for n, want := range map[string]bool{"pak01_dir.vpk": true, "pak99_dir.vpk": true, "pak1_dir.vpk": false,
		"pak01_000.vpk": false, "pak100_dir.vpk": false, "xpak01_dir.vpk": false, "pak01_dir.vpk.bak": false} {
		if IsPakName(n) != want {
			t.Errorf("%s: want %v", n, want)
		}
	}
}

func setupStore(t *testing.T) (Store, *State, string) {
	t.Helper()
	root := t.TempDir()
	st := Store{Dir: filepath.Join(root, "mods")}
	s := defaultState()
	addons := filepath.Join(root, "addons")
	for _, m := range []struct {
		id    string
		files []string
	}{{"a", []string{"a_dir.vpk"}}, {"b", []string{"b1_dir.vpk", "b2_dir.vpk"}}, {"c", []string{"c_dir.vpk"}}} {
		os.MkdirAll(st.ModDir(m.id), 0o755)
		for _, f := range m.files {
			os.WriteFile(filepath.Join(st.ModDir(m.id), f), testutil.FakeVPK(m.id+f), 0o644)
		}
		s.AddMod(&Mod{ID: m.id, Name: m.id, Enabled: true, Files: m.files})
	}
	return st, s, addons
}

func paksIn(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if len(b) >= 8 {
			b = b[8:]
		}
		out[e.Name()] = string(b)
	}
	return out
}

func TestApply(t *testing.T) {
	st, s, addons := setupStore(t)
	os.MkdirAll(addons, 0o755)
	os.WriteFile(filepath.Join(addons, "citadel.piw"), []byte("x"), 0o644)
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	got := paksIn(t, addons)
	if got["citadel.piw"] != "x" {
		t.Fatal("unrelated file touched")
	}
	want := map[string]string{"pak01_dir.vpk": "aa_dir.vpk", "pak02_dir.vpk": "bb1_dir.vpk", "pak03_dir.vpk": "bb2_dir.vpk", "pak04_dir.vpk": "cc_dir.vpk"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q want %q (all %v)", k, got[k], v, got)
		}
	}
	if !s.AppliedOnce || s.PendingApply || len(s.Mod("b").Paks) != 2 {
		t.Fatalf("state after apply: %+v", s)
	}

	// Disable b and move c first: paks renumber, stale pak04 removed.
	s.Mod("b").Enabled = false
	s.SetOrder([]string{"c", "a", "b"})
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	got = paksIn(t, addons)
	if got["pak01_dir.vpk"] != "cc_dir.vpk" || got["pak02_dir.vpk"] != "aa_dir.vpk" {
		t.Fatalf("after reorder: %v", got)
	}
	if _, ok := got["pak03_dir.vpk"]; ok {
		t.Fatal("stale pak03 not removed")
	}
	if _, ok := got["pak04_dir.vpk"]; ok {
		t.Fatal("stale pak04 not removed")
	}
	if len(s.Mod("b").Paks) != 0 {
		t.Fatal("disabled mod still has paks")
	}
}

func TestApplyRefusesUnknownAndImports(t *testing.T) {
	st, s, addons := setupStore(t)
	os.MkdirAll(addons, 0o755)
	os.WriteFile(filepath.Join(addons, "pak01_dir.vpk"), testutil.FakeVPK("foreign1"), 0o644)
	os.WriteFile(filepath.Join(addons, "pak05_dir.vpk"), testutil.FakeVPK("foreign5"), 0o644)
	err := st.Apply(s, addons)
	var unk *ErrUnknownPaks
	if !errors.As(err, &unk) || len(unk.Files) != 2 {
		t.Fatalf("expected unknown paks error, got %v", err)
	}
	if paksIn(t, addons)["pak01_dir.vpk"] != "foreign1" {
		t.Fatal("foreign file was touched")
	}
	added, err := st.ImportPaks(s, addons, unk.Files)
	if err != nil || len(added) != 2 {
		t.Fatal(added, err)
	}
	if _, ok := paksIn(t, addons)["pak01_dir.vpk"]; ok {
		t.Fatal("imported files should have moved out of addons")
	}
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	got := paksIn(t, addons)
	// a, b1, b2, c, then the imports in their old order.
	if got["pak05_dir.vpk"] != "foreign1" || got["pak06_dir.vpk"] != "foreign5" {
		t.Fatalf("imports not applied in order: %v", got)
	}

	// A file we wrote but someone changed afterwards counts as unknown again.
	os.WriteFile(filepath.Join(addons, "pak02_dir.vpk"), testutil.FakeVPK("tampered"), 0o644)
	if err := st.Apply(s, addons); !errors.As(err, &unk) || unk.Files[0] != "pak02_dir.vpk" {
		t.Fatalf("tampered file not detected: %v", err)
	}
}

func TestDesiredLimit(t *testing.T) {
	s := defaultState()
	files := make([]string, 100)
	for i := range files {
		files[i] = "x"
	}
	s.AddMod(&Mod{ID: "big", Enabled: true, Files: files})
	if _, err := Desired(s); err == nil {
		t.Fatal("expected error for >99 paks")
	}
}

func TestStateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := LoadState(p)
	if err != nil || !s.Settings.AutoUpdate || !s.Settings.StartWithWindows || !s.Settings.KeepGameinfo {
		t.Fatalf("defaults: %+v %v", s.Settings, err)
	}
	s.AddMod(&Mod{ID: "x", Name: "X", Enabled: true})
	s.AddMod(&Mod{ID: "y", Name: "Y"})
	s.SetOrder([]string{"y", "x"})
	s.Settings.AutoUpdate = false
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadState(p)
	if err != nil || s2.Mods[0].ID != "y" || s2.Mods[1].Order != 2 || s2.Settings.AutoUpdate {
		t.Fatalf("%+v %v", s2, err)
	}
	// AddMod with an existing id keeps position and enabled flag.
	s2.AddMod(&Mod{ID: "x", Name: "X2", Version: "2"})
	if m := s2.Mod("x"); m.Name != "X2" || !m.Enabled || m.Order != 2 {
		t.Fatalf("%+v", m)
	}
	os.WriteFile(p, []byte("{broken"), 0o644)
	if _, err := LoadState(p); err == nil {
		t.Fatal("expected error for corrupt state")
	}
}

// TestExtractRealArchives runs against real downloads when FROG_ARCHIVES is a
// list of paths (e.g. GameBanana .rar/.7z files); skipped otherwise.
func TestExtractRealArchives(t *testing.T) {
	list := os.Getenv("FROG_ARCHIVES")
	if list == "" {
		t.Skip("FROG_ARCHIVES not set")
	}
	for _, p := range filepath.SplitList(list) {
		names, err := ExtractVPKs(p, filepath.Join(t.TempDir(), "out"), filepath.Base(p))
		t.Logf("%s: %v %v", filepath.Base(p), names, err)
		if err != nil {
			t.Error(err)
		}
	}
}
