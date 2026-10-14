package mods

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
)

func TestDMMState(t *testing.T) {
	dir := t.TempDir()
	if u, l := DMMState(dir); u || l {
		t.Fatalf("no file: got inUse=%v leftover=%v", u, l)
	}
	write := func(s string) { os.WriteFile(filepath.Join(dir, dmmFile), []byte(s), 0o644) }
	write(`{"version":1,"mods":{}}`)
	if u, l := DMMState(dir); u || !l {
		t.Fatalf("empty file: got inUse=%v leftover=%v", u, l)
	}
	write(`{"version":3,"mods":{"abc":{"enabled":true}}}`)
	if u, l := DMMState(dir); !u || l {
		t.Fatalf("mods listed: got inUse=%v leftover=%v", u, l)
	}
	if err := RemoveDMMLeftover(dir); err != nil || !fsutil.Exists(filepath.Join(dir, dmmFile)) {
		t.Fatal("a file that lists mods must stay")
	}
	write(`not json`)
	if u, _ := DMMState(dir); !u {
		t.Fatal("unreadable file: assume DMM uses it")
	}
	write(`{"version":1,"mods":{}}`)
	if err := RemoveDMMLeftover(dir); err != nil || fsutil.Exists(filepath.Join(dir, dmmFile)) {
		t.Fatal("empty leftover should be removed")
	}
}
