// Package steam finds Deadlock: Steam libraries (VDF files), the app manifest
// and the game version.
package steam

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AppID = "1422450"

// VDF is a parsed Valve KeyValues node: values are either string or VDF.
type VDF map[string]any

// Get walks a path of keys (case-insensitive) and returns the string at the end.
func (v VDF) Get(path ...string) string {
	cur := v
	for i, k := range path {
		val, ok := cur.lookup(k)
		if !ok {
			return ""
		}
		if i == len(path)-1 {
			s, _ := val.(string)
			return s
		}
		sub, ok := val.(VDF)
		if !ok {
			return ""
		}
		cur = sub
	}
	return ""
}

// Sub returns a child node (case-insensitive) or nil.
func (v VDF) Sub(key string) VDF {
	val, _ := v.lookup(key)
	sub, _ := val.(VDF)
	return sub
}

func (v VDF) lookup(key string) (any, bool) {
	if val, ok := v[key]; ok {
		return val, true
	}
	for k, val := range v {
		if strings.EqualFold(k, key) {
			return val, true
		}
	}
	return nil, false
}

// ParseVDF parses the text KeyValues format used by Steam's .vdf/.acf files.
func ParseVDF(data string) (VDF, error) {
	toks, err := tokenizeVDF(data)
	if err != nil {
		return nil, err
	}
	pos := 0
	root, err := parseVDFBlock(toks, &pos, false)
	if err != nil {
		return nil, err
	}
	return root, nil
}

type vdfTok struct {
	s     string
	brace byte // '{' or '}' or 0 for a string
}

func tokenizeVDF(s string) ([]vdfTok, error) {
	var toks []vdfTok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '{' || c == '}':
			toks = append(toks, vdfTok{brace: c})
			i++
		case c == '"':
			var b strings.Builder
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(s[i])
					}
					continue
				}
				b.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("vdf: unterminated string")
			}
			i++
			toks = append(toks, vdfTok{s: b.String()})
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n{}\"", rune(s[j])) {
				j++
			}
			toks = append(toks, vdfTok{s: s[i:j]})
			i = j
		}
	}
	return toks, nil
}

func parseVDFBlock(toks []vdfTok, pos *int, nested bool) (VDF, error) {
	out := VDF{}
	for *pos < len(toks) {
		t := toks[*pos]
		if t.brace == '}' {
			if !nested {
				return nil, errors.New("vdf: unexpected }")
			}
			*pos++
			return out, nil
		}
		if t.brace == '{' {
			return nil, errors.New("vdf: unexpected {")
		}
		key := t.s
		*pos++
		if *pos >= len(toks) {
			return nil, fmt.Errorf("vdf: key %q without value", key)
		}
		v := toks[*pos]
		if v.brace == '{' {
			*pos++
			sub, err := parseVDFBlock(toks, pos, true)
			if err != nil {
				return nil, err
			}
			out[key] = sub
		} else if v.brace == '}' {
			return nil, fmt.Errorf("vdf: key %q without value", key)
		} else {
			out[key] = v.s
			*pos++
		}
	}
	if nested {
		return nil, errors.New("vdf: missing }")
	}
	return out, nil
}

// LibraryFolders returns the library roots listed in <steam>/steamapps/libraryfolders.vdf
// (the Steam root itself is always included first).
func LibraryFolders(steamRoot string) []string {
	libs := []string{steamRoot}
	data, err := os.ReadFile(filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libs
	}
	return appendUnique(libs, parseLibraryFolders(string(data))...)
}

func parseLibraryFolders(data string) []string {
	v, err := ParseVDF(data)
	if err != nil {
		return nil
	}
	lf := v.Sub("libraryfolders")
	if lf == nil {
		return nil
	}
	var out []string
	for _, val := range lf {
		switch e := val.(type) {
		case VDF:
			if p := e.Get("path"); p != "" {
				out = append(out, p)
			}
		case string: // very old format: "1" "D:\\Games"
			if strings.ContainsAny(e, `\/`) {
				out = append(out, e)
			}
		}
	}
	return out
}

func appendUnique(list []string, add ...string) []string {
	for _, a := range add {
		dup := false
		for _, l := range list {
			if strings.EqualFold(filepath.Clean(l), filepath.Clean(a)) {
				dup = true
				break
			}
		}
		if !dup && a != "" {
			list = append(list, a)
		}
	}
	return list
}

// GameInfo describes a detected Deadlock install.
type GameInfo struct {
	Dir           string `json:"dir"` // the folder containing game/citadel
	BuildID       string `json:"buildId"`
	ClientVersion string `json:"clientVersion"`
}

// FindGame searches all Steam libraries for the Deadlock appmanifest.
func FindGame() (string, error) {
	var roots []string
	roots = appendUnique(roots, steamRootsFromRegistry()...)
	roots = appendUnique(roots, `C:\Program Files (x86)\Steam`, `C:\Program Files\Steam`)
	for _, root := range roots {
		for _, lib := range LibraryFolders(root) {
			manifest := filepath.Join(lib, "steamapps", "appmanifest_"+AppID+".acf")
			data, err := os.ReadFile(manifest)
			if err != nil {
				continue
			}
			v, err := ParseVDF(string(data))
			if err != nil {
				continue
			}
			inst := v.Get("AppState", "installdir")
			if inst == "" {
				inst = "Deadlock"
			}
			dir := filepath.Join(lib, "steamapps", "common", inst)
			if IsGameDir(dir) {
				return dir, nil
			}
		}
	}
	return "", errors.New("Deadlock not found in any Steam library")
}

// IsGameDir reports whether dir looks like a Deadlock install.
func IsGameDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "game", "citadel", "gameinfo.gi"))
	return err == nil && !st.IsDir()
}

// NormalizeGameDir accepts the game root or a subfolder (game, game/citadel,
// game/citadel/addons) and returns the root.
func NormalizeGameDir(dir string) string {
	dir = filepath.Clean(strings.Trim(strings.TrimSpace(dir), `"`))
	for i := 0; i < 4 && dir != "" && dir != "."; i++ {
		if IsGameDir(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// ReadGameInfo reads build id (from the appmanifest next to the library) and
// ClientVersion from steam.inf. Missing values are left empty.
func ReadGameInfo(dir string) GameInfo {
	gi := GameInfo{Dir: dir}
	// <lib>/steamapps/common/<installdir> -> <lib>/steamapps/appmanifest_X.acf
	manifest := filepath.Join(filepath.Dir(filepath.Dir(dir)), "appmanifest_"+AppID+".acf")
	if data, err := os.ReadFile(manifest); err == nil {
		if v, err := ParseVDF(string(data)); err == nil {
			gi.BuildID = v.Get("AppState", "buildid")
			if gi.BuildID == "" {
				gi.BuildID = v.Get("AppState", "installed_build_id")
			}
		}
	}
	if f, err := os.Open(filepath.Join(dir, "game", "citadel", "steam.inf")); err == nil {
		gi.ClientVersion = parseSteamInf(f)["ClientVersion"]
		f.Close()
	}
	return gi
}

func parseSteamInf(f *os.File) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func Paths(dir string) (citadel, addons, gameinfo string) {
	citadel = filepath.Join(dir, "game", "citadel")
	return citadel, filepath.Join(citadel, "addons"), filepath.Join(citadel, "gameinfo.gi")
}
