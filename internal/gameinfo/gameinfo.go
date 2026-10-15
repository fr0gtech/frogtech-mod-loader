// Package gameinfo keeps mods switched on: it makes sure game/citadel/gameinfo.gi
// mounts citadel/addons and keeps the game's own Mod/Write folders (game
// updates and Steam's file check reset it), and can take those lines out again.
package gameinfo

import (
	"errors"
	"os"
	"strings"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
)

// The game's gameinfo.gi lists the folders it loads content from in a
// FileSystem { SearchPaths { ... } } block. Mods in game/citadel/addons are only
// loaded when that block mounts "citadel/addons" before "citadel". Every game
// update rewrites the file to vanilla, so we re-add the lines.

const AddonsPath = "citadel/addons"

type searchPathLine struct {
	idx   int    // index into lines
	key   string // e.g. Game, Game_LowViolence, Mod
	value string
}

type searchPaths struct {
	open, close int // line indexes of { and }
	entries     []searchPathLine
}

// splitLines splits keeping each line's terminator (\r\n, \n or none).
func splitLines(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func stripComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

func findSearchPaths(lines []string) (*searchPaths, error) {
	for i, l := range lines {
		if !strings.EqualFold(stripComment(l), "SearchPaths") {
			continue
		}
		j := i + 1
		for j < len(lines) && stripComment(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || !strings.HasPrefix(stripComment(lines[j]), "{") {
			return nil, errors.New("gameinfo.gi: SearchPaths without {")
		}
		sp := &searchPaths{open: j}
		depth := 1
		for k := j + 1; k < len(lines); k++ {
			c := stripComment(lines[k])
			if c == "" {
				continue
			}
			if strings.HasPrefix(c, "{") {
				depth++
				continue
			}
			if strings.HasPrefix(c, "}") {
				depth--
				if depth == 0 {
					sp.close = k
					return sp, nil
				}
				continue
			}
			if depth == 1 {
				f := strings.Fields(c)
				e := searchPathLine{idx: k, key: strings.Trim(f[0], `"`)}
				if len(f) > 1 {
					e.value = strings.Trim(f[1], `"`)
				}
				sp.entries = append(sp.entries, e)
			}
		}
		return nil, errors.New("gameinfo.gi: SearchPaths block not closed")
	}
	return nil, errors.New("gameinfo.gi: no SearchPaths block found")
}

func isAddonsValue(v string) bool {
	v = strings.ToLower(strings.ReplaceAll(v, `\`, "/"))
	return strings.Contains(v, AddonsPath)
}

// The engine picks its MOD and DEFAULT_WRITE_PATH search paths from the first
// "Game" line when the block has no Mod or Write lines, which is the case in
// Valve's stock file. With "Game citadel/addons" first, that makes the mods
// folder the place the game reads its own defaults from and writes to: it
// then reports its files as damaged and stops with "Unable to read default
// keybinding configuration". These lines, added after the last Game line,
// keep both pointed at the game's own folders.
var writeLines = []struct{ key, value string }{
	{"Mod", "citadel"},
	{"Write", "citadel"},
	{"Mod", "core"},
	{"Write", "core"},
}

func isWriteLine(e searchPathLine) bool {
	for _, w := range writeLines {
		if strings.EqualFold(e.key, w.key) && strings.EqualFold(e.value, w.value) {
			return true
		}
	}
	return false
}

// Report says what the SearchPaths block has.
type Report struct {
	Addons       bool     `json:"addons"`       // mounts citadel/addons
	MissingWrite []string `json:"missingWrite"` // Mod/Write lines that are missing, e.g. "Write citadel"
}

// Patched reports whether mods load and the game can start with them.
func (r Report) Patched() bool { return r.Addons && len(r.MissingWrite) == 0 }

// Check reports what the SearchPaths block in content has.
func Check(content string) (Report, error) {
	sp, err := findSearchPaths(splitLines(content))
	if err != nil {
		return Report{}, err
	}
	return check(sp), nil
}

func check(sp *searchPaths) Report {
	r := Report{MissingWrite: []string{}}
	for _, e := range sp.entries {
		if isAddonsValue(e.value) {
			r.Addons = true
		}
	}
	for _, w := range writeLines {
		found := false
		for _, e := range sp.entries {
			if strings.EqualFold(e.key, w.key) && strings.EqualFold(e.value, w.value) {
				found = true
				break
			}
		}
		if !found {
			r.MissingWrite = append(r.MissingWrite, w.key+" "+w.value)
		}
	}
	return r
}

// HasAddons reports whether the SearchPaths block mounts citadel/addons.
func HasAddons(content string) (bool, error) {
	r, err := Check(content)
	return r.Addons, err
}

// Ensure returns content with a "Game citadel/addons" search path inserted as
// the first non-language entry (before Game/Mod citadel), and the Mod/Write
// lines that are missing added after the last entry. New lines copy the
// indentation, key/value spacing and line ending of the line they sit next
// to. changed is false when nothing was missing.
func Ensure(content string) (out string, changed bool, err error) {
	lines := splitLines(content)
	sp, err := findSearchPaths(lines)
	if err != nil {
		return content, false, err
	}
	r := check(sp)
	if r.Patched() {
		return content, false, nil
	}
	insert := map[int][]string{} // line index -> new lines to put before it
	if !r.Addons {
		at := sp.close
		for _, e := range sp.entries {
			if !strings.HasPrefix(strings.ToLower(e.key), "game_") {
				at = e.idx
				break
			}
		}
		insert[at] = append(insert[at], newLine(lines, sp, at, "Game", AddonsPath))
	}
	if len(r.MissingWrite) > 0 {
		ref := sp.close
		if n := len(sp.entries); n > 0 {
			ref = sp.entries[n-1].idx
		}
		at := ref + 1 // right after the last entry (before "}" when the block is empty)
		if ref == sp.close {
			at = sp.close
		}
		for _, m := range r.MissingWrite {
			k, v, _ := strings.Cut(m, " ")
			insert[at] = append(insert[at], newLine(lines, sp, ref, k, v))
		}
	}
	res := make([]string, 0, len(lines)+5)
	for i, l := range lines {
		res = append(res, insert[i]...)
		res = append(res, l)
	}
	return strings.Join(res, ""), true, nil
}

// EnsureAddonsLine is Ensure (kept for callers of the old name).
func EnsureAddonsLine(content string) (string, bool, error) { return Ensure(content) }

// newLine builds "<indent>key<sep>value<eol>" in the style of lines[ref].
func newLine(lines []string, sp *searchPaths, ref int, key, value string) string {
	l := lines[ref]
	indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
	sep := "\t\t\t\t"
	eol := lineEnding(l, lines)
	if ref != sp.close {
		// Copy the whitespace between key and value: "\t\t\tGame\t\t\t\tcitadel".
		rest := strings.TrimLeft(l, " \t")
		if k := strings.IndexAny(rest, " \t"); k > 0 {
			after := rest[k:]
			sep = after[:len(after)-len(strings.TrimLeft(after, " \t"))]
		}
	} else {
		// Next to "}": indent one level deeper than the brace.
		indent += "\t"
	}
	return indent + key + sep + value + eol
}

// Strip undoes Ensure: it removes every citadel/addons line and the Mod/Write
// lines at the end of the block (where Ensure puts them). Mod/Write lines
// between Game lines (Deadlock Mod Manager's layout) are left alone. On a file
// Ensure patched, the result is the original byte for byte.
func Strip(content string) (out string, changed bool, err error) {
	lines := splitLines(content)
	sp, err := findSearchPaths(lines)
	if err != nil {
		return content, false, err
	}
	drop := map[int]bool{}
	for i := len(sp.entries) - 1; i >= 0 && isWriteLine(sp.entries[i]); i-- {
		drop[sp.entries[i].idx] = true
	}
	for _, e := range sp.entries {
		if isAddonsValue(e.value) {
			drop[e.idx] = true
		}
	}
	if len(drop) == 0 {
		return content, false, nil
	}
	res := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i] {
			res = append(res, l)
		}
	}
	return strings.Join(res, ""), true, nil
}

func lineEnding(ref string, all []string) string {
	if strings.HasSuffix(ref, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(ref, "\n") {
		return "\n"
	}
	for _, l := range all {
		if strings.HasSuffix(l, "\r\n") {
			return "\r\n"
		}
	}
	return "\n"
}

// Fix makes sure the game's gameinfo.gi mounts the addons folder (with the
// Mod/Write lines the game needs then) and that the folder exists. It backs
// the original up once to gameinfo.gi.bak and writes atomically. Returns true
// if the file was modified.
func Fix(gameDir string) (bool, error) {
	_, addons, _ := steam.Paths(gameDir)
	if err := os.MkdirAll(addons, 0o755); err != nil {
		return false, err
	}
	return rewrite(gameDir, Ensure)
}

// Unfix takes the loader's lines out again, leaving Valve's file as it was:
// the game then loads no mods. Returns true if the file was modified.
func Unfix(gameDir string) (bool, error) {
	return rewrite(gameDir, Strip)
}

func rewrite(gameDir string, f func(string) (string, bool, error)) (bool, error) {
	_, _, path := steam.Paths(gameDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, changed, err := f(string(data))
	if err != nil || !changed {
		return false, err
	}
	bak := path + ".bak"
	if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(bak, data, 0o644); err != nil {
			return false, err
		}
	}
	if err := fsutil.WriteFileAtomic(path, []byte(out)); err != nil {
		return false, err
	}
	return true, nil
}

// Status reports what the game's gameinfo.gi has.
func Status(gameDir string) (Report, error) {
	_, _, path := steam.Paths(gameDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{MissingWrite: []string{}}, err
	}
	return Check(string(data))
}
