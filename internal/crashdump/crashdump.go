// Package crashdump notices when Deadlock fails to start: the game writes a
// minidump (game/bin/win64/deadlock_<date>_<time>_0_<kind>.mdmp) when it stops
// with an error, and the dump carries the error text ("FATAL ERROR: ...").
package crashdump

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dump is one minidump the game wrote.
type Dump struct {
	Name string    `json:"name"`
	Path string    `json:"-"`
	Time time.Time `json:"time"`
	// Error is true for "_error" dumps: the game stopped itself with a fatal
	// error (as opposed to a crash such as "_accessviolation").
	Error bool `json:"error"`
}

// Dir is where the game writes its dumps.
func Dir(gameDir string) string { return filepath.Join(gameDir, "game", "bin", "win64") }

// Find lists the game's dumps written after since, newest first.
func Find(gameDir string, since time.Time) []Dump {
	dir := Dir(gameDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Dump
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasPrefix(n, "deadlock_") || !strings.HasSuffix(n, ".mdmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().After(since) {
			continue
		}
		out = append(out, Dump{
			Name:  e.Name(),
			Path:  filepath.Join(dir, e.Name()),
			Time:  info.ModTime(),
			Error: strings.HasSuffix(n, "_error.mdmp"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// maxRead bounds how much of a dump is searched (the game's are 0.2-2 MB).
const maxRead = 32 << 20

var marker = []byte("FATAL ERROR:")

// Message returns the game's error text from a dump ("Unable to read default
// keybinding configuration (user_keys_default)!"), or "" if it has none. The
// text appears several times, sometimes cut short; the most common (then the
// longest) copy wins.
func Message(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRead))
	if err != nil {
		return ""
	}
	return MessageFrom(data)
}

// MessageFrom finds the error text in a dump's bytes (ASCII or UTF-16).
func MessageFrom(data []byte) string {
	count := map[string]int{}
	collect := func(b []byte, step int, mark []byte) {
		for off := 0; ; {
			i := bytes.Index(b[off:], mark)
			if i < 0 {
				return
			}
			start := off + i + len(mark)
			var sb strings.Builder
			for k := start; k < len(b) && sb.Len() < 300; k += step {
				c := b[k]
				if c < 0x20 || c > 0x7e || (step == 2 && (k+1 >= len(b) || b[k+1] != 0)) {
					break
				}
				sb.WriteByte(c)
			}
			if s := strings.TrimSpace(sb.String()); len(s) >= 4 {
				count[s]++
			}
			off = start
		}
	}
	collect(data, 1, marker)
	wide := make([]byte, 0, 2*len(marker))
	for _, c := range marker {
		wide = append(wide, c, 0)
	}
	collect(data, 2, wide)
	best := ""
	for s, n := range count {
		if n > count[best] || (n == count[best] && len(s) > len(best)) {
			best = s
		}
	}
	return best
}
