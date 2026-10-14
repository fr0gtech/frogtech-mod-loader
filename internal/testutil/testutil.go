// Package testutil has helpers shared by the packages' tests: fixtures from
// internal/testdata, fake VPKs and zips, and a fake Steam library.
package testutil

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// Dir is the absolute path of internal/testdata.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

// Read returns a fixture from internal/testdata.
func Read(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// FakeVPK returns bytes with a valid VPK v2 signature followed by tag.
func FakeVPK(tag string) []byte {
	return append([]byte{0x34, 0x12, 0xaa, 0x55, 2, 0, 0, 0}, []byte(tag)...)
}

// WriteZip writes a zip with the given files (sorted by name).
func WriteZip(t testing.TB, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write(files[n])
	}
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// FakeLibrary builds <lib>/steamapps/{appmanifest,common/Deadlock/game/citadel}
// from the fixtures and returns the library and game folders.
func FakeLibrary(t testing.TB) (lib, game string) {
	t.Helper()
	lib = t.TempDir()
	sa := filepath.Join(lib, "steamapps")
	game = filepath.Join(sa, "common", "Deadlock")
	cit := filepath.Join(game, "game", "citadel")
	os.MkdirAll(cit, 0o755)
	os.WriteFile(filepath.Join(sa, "appmanifest_1422450.acf"), []byte(Read(t, "appmanifest_1422450.acf")), 0o644)
	os.WriteFile(filepath.Join(cit, "steam.inf"), []byte(Read(t, "steam.inf")), 0o644)
	os.WriteFile(filepath.Join(cit, "gameinfo.gi"), []byte(Read(t, "gameinfo_vanilla.gi")), 0o644)
	return lib, game
}
