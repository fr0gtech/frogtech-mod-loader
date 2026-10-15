package crashdump

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDump looks like the game's minidumps: binary noise with the error text
// in a few places, once cut short, once as UTF-16.
func fakeDump(msg string) []byte {
	var b []byte
	noise := func() { b = append(b, 0x4d, 0x44, 0x4d, 0x50, 0x93, 0xa7, 0, 0, 0xff, 0x01) }
	noise()
	b = append(b, "AbortMessage({FATAL ERROR: "+msg...)
	b = append(b, 0, 0)
	noise()
	b = append(b, "FATAL ERROR: "+msg[:12]...)
	b = append(b, 0xc3)
	noise()
	for _, c := range "FATAL ERROR: " + msg {
		b = append(b, byte(c), 0)
	}
	b = append(b, 0, 0)
	noise()
	b = append(b, "66(1.790302):  FATAL ERROR: "+msg+"\n"...)
	noise()
	return b
}

func TestMessage(t *testing.T) {
	msg := "Unable to read default keybinding configuration (user_keys_default)!"
	if got := MessageFrom(fakeDump(msg)); got != msg {
		t.Fatalf("got %q", got)
	}
	if got := MessageFrom([]byte("nothing here FATAL ERROR:\x00")); got != "" {
		t.Fatalf("empty marker gave %q", got)
	}
	if got := MessageFrom(nil); got != "" {
		t.Fatal(got)
	}
	// UTF-16 only.
	var w []byte
	for _, c := range "FATAL ERROR: Out of memory" {
		w = append(w, byte(c), 0)
	}
	if got := MessageFrom(append(w, 0, 0)); got != "Out of memory" {
		t.Fatalf("utf16: %q", got)
	}
}

func TestFind(t *testing.T) {
	game := t.TempDir()
	dir := Dir(game)
	os.MkdirAll(dir, 0o755)
	launch := time.Now().Add(-time.Minute)
	write := func(name string, at time.Time, data []byte) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		os.Chtimes(p, at, at)
	}
	write("deadlock_2026_1015_041239_0_accessviolation.mdmp", launch.Add(-time.Hour), []byte("x"))
	write("deadlock_2026_1015_051540_0_error.mdmp", launch.Add(20*time.Second), fakeDump("Unable to read default keybinding configuration (user_keys_default)!"))
	write("deadlock_2026_1015_051541_0_accessviolation.mdmp", launch.Add(30*time.Second), []byte("y"))
	write("steam_appid.txt", launch.Add(time.Second), []byte("1422450"))
	write("other_error.mdmp", launch.Add(time.Second), []byte("z"))

	got := Find(game, launch)
	if len(got) != 2 || got[0].Name != "deadlock_2026_1015_051541_0_accessviolation.mdmp" || got[0].Error || !got[1].Error {
		t.Fatalf("%+v", got)
	}
	if m := Message(got[1].Path); m != "Unable to read default keybinding configuration (user_keys_default)!" {
		t.Fatalf("message %q", m)
	}
	if len(Find(game, time.Now().Add(time.Hour))) != 0 || Find(filepath.Join(game, "nope"), launch) != nil {
		t.Fatal("expected nothing")
	}
	if Message(filepath.Join(dir, "missing.mdmp")) != "" {
		t.Fatal("missing file")
	}
}
