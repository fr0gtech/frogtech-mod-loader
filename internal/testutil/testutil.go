// Package testutil has helpers shared by the packages' tests: fixtures from
// internal/testdata, fake VPKs and zips, and a fake Steam library.
package testutil

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
	os.WriteFile(filepath.Join(cit, "gameinfo.gi"), []byte(Read(t, "gameinfo_stock_6785.gi")), 0o644)
	return lib, game
}

// BuildVPK returns a single-file VPK v2 (directory tree with the data inline
// as preload bytes) holding files, e.g. {"panorama/layout/x.xml": ...}.
func BuildVPK(files map[string][]byte) []byte {
	type entry struct {
		dir, name string
		data      []byte
	}
	byExt := map[string][]entry{}
	for p, data := range files {
		dir, file := filepath.Split(p)
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" {
			dir = " "
		}
		ext := filepath.Ext(file)
		byExt[strings.TrimPrefix(ext, ".")] = append(byExt[strings.TrimPrefix(ext, ".")], entry{dir, strings.TrimSuffix(file, ext), data})
	}
	var tree bytes.Buffer
	cstr := func(s string) { tree.WriteString(s); tree.WriteByte(0) }
	exts := make([]string, 0, len(byExt))
	for e := range byExt {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	for _, ext := range exts {
		cstr(ext)
		byDir := map[string][]entry{}
		var dirs []string
		for _, e := range byExt[ext] {
			if byDir[e.dir] == nil {
				dirs = append(dirs, e.dir)
			}
			byDir[e.dir] = append(byDir[e.dir], e)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			cstr(d)
			for _, e := range byDir[d] {
				cstr(e.name)
				binary.Write(&tree, binary.LittleEndian, struct {
					CRC          uint32
					PreloadBytes uint16
					ArchiveIndex uint16
					Offset, Len  uint32
					Terminator   uint16
				}{0, uint16(len(e.data)), 0x7fff, 0, 0, 0xffff})
				tree.Write(e.data)
			}
			cstr("")
		}
		cstr("")
	}
	cstr("")
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [7]uint32{0x55aa1234, 2, uint32(tree.Len()), 0, 0, 0, 0})
	out.Write(tree.Bytes())
	return out.Bytes()
}
