package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The game's gameinfo.gi lists the folders it loads content from in a
// FileSystem { SearchPaths { ... } } block. Mods in game/citadel/addons are only
// loaded when that block mounts "citadel/addons" before "citadel". Every game
// update rewrites the file to vanilla, so we re-add the line.

const addonsPath = "citadel/addons"

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
	return strings.Contains(v, addonsPath)
}

// GameinfoHasAddons reports whether the SearchPaths block mounts citadel/addons.
func GameinfoHasAddons(content string) (bool, error) {
	sp, err := findSearchPaths(splitLines(content))
	if err != nil {
		return false, err
	}
	for _, e := range sp.entries {
		if isAddonsValue(e.value) {
			return true, nil
		}
	}
	return false, nil
}

// EnsureAddonsLine returns content with a "Game citadel/addons" search path
// inserted as the first non-language entry (before Game/Mod citadel). The new
// line copies the indentation, key/value spacing and line ending of the line it
// is inserted before. changed is false when the line was already present.
func EnsureAddonsLine(content string) (out string, changed bool, err error) {
	lines := splitLines(content)
	sp, err := findSearchPaths(lines)
	if err != nil {
		return content, false, err
	}
	for _, e := range sp.entries {
		if isAddonsValue(e.value) {
			return content, false, nil
		}
	}
	at := sp.close
	for _, e := range sp.entries {
		if !strings.HasPrefix(strings.ToLower(e.key), "game_") {
			at = e.idx
			break
		}
	}
	ref := lines[at]
	indent := ref[:len(ref)-len(strings.TrimLeft(ref, " \t"))]
	sep := "\t\t\t\t"
	eol := lineEnding(ref, lines)
	if at != sp.close {
		// Copy the whitespace between key and value: "\t\t\tGame\t\t\t\tcitadel".
		rest := strings.TrimLeft(ref, " \t")
		if k := strings.IndexAny(rest, " \t"); k > 0 {
			after := rest[k:]
			sep = after[:len(after)-len(strings.TrimLeft(after, " \t"))]
		}
	} else {
		// Inserting before "}": indent one level deeper than the brace.
		indent += "\t"
	}
	newLine := indent + "Game" + sep + addonsPath + eol
	res := make([]string, 0, len(lines)+1)
	res = append(res, lines[:at]...)
	res = append(res, newLine)
	res = append(res, lines[at:]...)
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

// FixGameinfo makes sure the game's gameinfo.gi mounts the addons folder and
// that the folder exists. It backs the original up once to gameinfo.gi.bak and
// writes atomically. Returns true if the file was modified.
func FixGameinfo(gameDir string) (bool, error) {
	_, addons, path := gamePaths(gameDir)
	if err := os.MkdirAll(addons, 0o755); err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, changed, err := EnsureAddonsLine(string(data))
	if err != nil || !changed {
		return false, err
	}
	bak := path + ".bak"
	if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(bak, data, 0o644); err != nil {
			return false, err
		}
	}
	if err := writeFileAtomic(path, []byte(out)); err != nil {
		return false, err
	}
	return true, nil
}

// GameinfoStatus returns ok=true when the addons line is present.
func GameinfoStatus(gameDir string) (bool, error) {
	_, _, path := gamePaths(gameDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return GameinfoHasAddons(string(data))
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
