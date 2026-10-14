package mods

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

func TestSummarizeVPK(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pak01_dir.vpk")
	os.WriteFile(p, testutil.BuildVPK(map[string][]byte{
		"panorama/scripts/steam_profiles/main.vjs_c": []byte("a"),
		"panorama/scripts/steam_profiles/menu.vjs_c": []byte("b"),
		"panorama/layout/post_game/x.vxml_c":         []byte("c"),
		"panorama/layout/y.vxml_c":                   []byte("d"),
		"readme.txt":                                 []byte("e"),
	}), 0o644)
	s, err := SummarizeVPK(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Files != 5 || len(s.Folders) != 3 || s.Folders[0] != "panorama/scripts/steam_profiles" {
		t.Fatalf("%+v", s)
	}
	// Not a VPK, truncated, or a lying header: errors, never panics.
	bad := map[string][]byte{
		"text":      []byte("hello world, not a vpk"),
		"short":     {0x34, 0x12},
		"truncated": testutil.BuildVPK(map[string][]byte{"a/b.txt": []byte("x")})[:40],
		"version 9": append([]byte{0x34, 0x12, 0xaa, 0x55, 9, 0, 0, 0}, make([]byte, 20)...),
		"huge tree": {0x34, 0x12, 0xaa, 0x55, 1, 0, 0, 0, 0xff, 0xff, 0xff, 0x7f},
	}
	for name, data := range bad {
		os.WriteFile(p, data, 0o644)
		if _, err := SummarizeVPK(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A dev script copies a test build over a pak the loader wrote: the details
// name the mod it replaced; a brand-new pak has no "replaces".
func TestDescribeUnknownAndKeep(t *testing.T) {
	st, s, addons := setupStore(t)
	s.Mod("b").Version = "1.1"
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	testBuild := testutil.BuildVPK(map[string][]byte{"panorama/scripts/steam_profiles/main.vjs_c": []byte("test build 1.2")})
	os.WriteFile(filepath.Join(addons, "pak02_dir.vpk"), testBuild, 0o644)           // was b's first file
	os.WriteFile(filepath.Join(addons, "pak09_dir.vpk"), fakeVPKBytes("new"), 0o644) // nobody's slot
	d, err := DescribeUnknown(addons, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 2 || d[0].Name != "pak02_dir.vpk" || d[1].Name != "pak09_dir.vpk" {
		t.Fatalf("%+v", d)
	}
	if r := d[0].Replaces; r == nil || r.ID != "b" || r.Version != "1.1" {
		t.Fatalf("replaces: %+v", r)
	}
	if d[0].Size != int64(len(testBuild)) || d[0].Modified.IsZero() || d[0].Contents == nil || d[0].Contents.Files != 1 {
		t.Fatalf("details: %+v", d[0])
	}
	if d[1].Replaces != nil {
		t.Fatalf("new file has replaces: %+v", d[1].Replaces)
	}

	// Keep the test build: it becomes a mod in b's place and b is turned off.
	m, err := st.KeepPak(s, addons, "pak02_dir.vpk")
	if err != nil {
		t.Fatal(err)
	}
	if s.Mod("b").Enabled || !m.Enabled || !strings.HasPrefix(m.Name, "b ") {
		t.Fatalf("kept %+v, b enabled=%v", m, s.Mod("b").Enabled)
	}
	order := []string{}
	for _, x := range s.Mods {
		order = append(order, x.ID)
	}
	if len(order) != 4 || order[0] != "a" || order[1] != m.ID || order[2] != "b" || order[3] != "c" {
		t.Fatalf("order %v", order)
	}
	// Only the brand-new pak is still foreign; after it's handled, apply works
	// and the kept build is what the game loads.
	os.Remove(filepath.Join(addons, "pak09_dir.vpk"))
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(addons, "pak02_dir.vpk"))
	if string(got) != string(testBuild) {
		t.Fatal("kept build not applied in b's slot")
	}
	if _, err := st.KeepPak(s, addons, "pak02_dir.vpk"); err == nil {
		t.Fatal("kept a file that's no longer foreign")
	}
}

func fakeVPKBytes(tag string) []byte { return testutil.FakeVPK(tag) }

// A pak the loader wrote that has gone missing (deleted by hand or by
// "Put back") is written again on the next apply.
func TestApplyRestoresDeletedPak(t *testing.T) {
	st, s, addons := setupStore(t)
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(addons, "pak02_dir.vpk")
	want, _ := os.ReadFile(p)
	os.Remove(p)
	if err := st.Apply(s, addons); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != string(want) {
		t.Fatalf("pak02 not restored: %v", err)
	}
}
