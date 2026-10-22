// Package mods owns the loader's state and the addons folder: installed mods
// (state.json), their stored VPKs, extracting archives, and writing enabled
// mods into the game as pak01_dir.vpk, pak02_dir.vpk ...
package mods

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
	"github.com/nwaples/rardecode/v2"
)

// Sources of a mod.
const (
	SourceCatalog    = "catalog"
	SourceGameBanana = "gamebanana"
	SourceLocal      = "local"
	SourceRepo       = "repo" // a third-party repository (id "<repoId>/<modId>")
)

// Mod is one installed mod. Its VPKs live in <data>/mods/<ID>/.
type Mod struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Name        string    `json:"name"`
	Version     string    `json:"version,omitempty"`
	Enabled     bool      `json:"enabled"`
	Order       int       `json:"order"`
	Files       []string  `json:"files"`              // VPK file names inside the mod's store folder
	Paks        []string  `json:"paks,omitempty"`     // pakNN_dir.vpk names at the last apply
	Page        string    `json:"page,omitempty"`     // web page for the mod
	Image       string    `json:"image,omitempty"`    // preview image URL (catalog or GameBanana)
	Category    string    `json:"category,omitempty"` // GameBanana category (e.g. "Skins")
	InstalledAt time.Time `json:"installedAt"`

	// Repository (SourceRepo)
	Repo    string `json:"repo,omitempty"`
	RepoMod string `json:"repoMod,omitempty"`

	// GameBanana
	GBID       int    `json:"gbId,omitempty"`
	GBModel    string `json:"gbModel,omitempty"` // "Mod" (default) or "Sound"
	GBFileID   int    `json:"gbFileId,omitempty"`
	GBFileName string `json:"gbFileName,omitempty"`
	GBModified int64  `json:"gbModified,omitempty"`
	GBPinned   bool   `json:"gbPinned,omitempty"` // user picked a specific (non-newest) file: no auto-update
}

// AppliedPak records a file the loader wrote into the addons folder.
type AppliedPak struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	ModID  string `json:"modId"`
}

// Settings are user preferences.
type Settings struct {
	AutoUpdate       bool `json:"autoUpdate"`
	StartWithWindows bool `json:"startWithWindows"`
	KeepGameinfo     bool `json:"keepGameinfo"`
	ShowMature       bool `json:"showMature"` // don't blur GameBanana items it marks as mature
	QueueAlert       bool `json:"queueAlert"` // alert when Deadlock finds a match (internal/queuealert)
	// DontAsk lists confirmation questions the user ticked "Don't ask again" on
	// (keys like "remove", "import", "deleteForeign"; see web/index.html).
	DontAsk map[string]bool `json:"dontAsk,omitempty"`
}

// State is persisted to <data>/state.json.
type State struct {
	GameDir      string       `json:"gameDir,omitempty"`
	Settings     Settings     `json:"settings"`
	Mods         []*Mod       `json:"mods"`
	Applied      []AppliedPak `json:"applied"`
	AppliedOnce  bool         `json:"appliedOnce"`
	PendingApply bool         `json:"pendingApply"`
	RunKeyInit   bool         `json:"runKeyInit"` // the start-with-Windows default was applied once
	// QueueAlertInit: match alerts were switched on once (the default)
	QueueAlertInit bool `json:"queueAlertInit"`
	// AlertKey links phones to match alerts on mods.frogtech.dev (sealed, internal/secrets)
	AlertKey string      `json:"alertKey,omitempty"`
	Window   *WindowRect `json:"window,omitempty"`
	// NotifiedLoader is the loader version a "new version" notification was
	// shown for, so background checks announce each version once.
	NotifiedLoader string `json:"notifiedLoader,omitempty"`
	// Repos are the user's added repositories (Frogtech's is built in).
	Repos            []*Repo `json:"repos,omitempty"`
	FrogtechDisabled bool    `json:"frogtechDisabled,omitempty"`
	// ModsOff is the main switch: no mod files in the game and Valve's own
	// gameinfo.gi, until it is turned back on.
	ModsOff bool `json:"modsOff,omitempty"`
	// StartSeen: game crash dumps up to this time were already shown (or
	// the game started fine after them).
	StartSeen time.Time `json:"startSeen,omitempty"`
	// CorruptSeen is the appmanifest LastUpdated at which the user dismissed
	// Steam's "files damaged" flag.
	CorruptSeen string `json:"corruptSeen,omitempty"`
	// Repair is set while Steam checks the game files for "Repair".
	Repair *Repair `json:"repair,omitempty"`
	// NoModsRun is set while the game runs once without mods.
	NoModsRun *NoModsRun `json:"noModsRun,omitempty"`
}

// Repair tracks a running repair: mods are out and Steam checks the files.
type Repair struct {
	Started     time.Time `json:"started"`
	LastUpdated string    `json:"lastUpdated"` // appmanifest LastUpdated when it began
	SawCheck    bool      `json:"sawCheck"`    // Steam was seen working on the game
}

// NoModsRun tracks "Start without mods": gameinfo.gi is Valve's until the game
// has run and closed again.
type NoModsRun struct {
	Started    time.Time `json:"started"`
	SawRunning bool      `json:"sawRunning"`
}

// WindowRect is the loader window's last normal position (physical pixels).
type WindowRect struct {
	X, Y, W, H int
	Maximized  bool
}

func defaultState() *State {
	return &State{Settings: Settings{AutoUpdate: true, StartWithWindows: true, KeepGameinfo: true}}
}

// LoadState reads state.json, returning defaults if it doesn't exist.
func LoadState(path string) (*State, error) {
	st := defaultState()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return defaultState(), fmt.Errorf("state.json is corrupt: %w", err)
	}
	st.normalize()
	return st, nil
}

// Save writes state.json atomically.
func (s *State) Save(path string) error {
	s.normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data)
}

// normalize sorts mods by Order and renumbers 1..n.
func (s *State) normalize() {
	sort.SliceStable(s.Mods, func(i, j int) bool { return s.Mods[i].Order < s.Mods[j].Order })
	for i, m := range s.Mods {
		m.Order = i + 1
	}
}

func (s *State) Mod(id string) *Mod {
	for _, m := range s.Mods {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (s *State) RemoveMod(id string) {
	out := s.Mods[:0]
	for _, m := range s.Mods {
		if m.ID != id {
			out = append(out, m)
		}
	}
	s.Mods = out
	s.normalize()
}

// AddMod appends a mod (lowest priority) or replaces one with the same ID,
// keeping its position and enabled flag.
func (s *State) AddMod(m *Mod) {
	if old := s.Mod(m.ID); old != nil {
		m.Order = old.Order
		m.Enabled = old.Enabled
		*old = *m
		return
	}
	m.Order = len(s.Mods) + 1
	s.Mods = append(s.Mods, m)
	s.normalize()
}

// SetOrder reorders mods by the given ids; unknown ids are ignored and missing
// mods keep their relative order at the end.
func (s *State) SetOrder(ids []string) {
	pos := map[string]int{}
	for i, id := range ids {
		pos[id] = i + 1
	}
	for _, m := range s.Mods {
		if p, ok := pos[m.ID]; ok {
			m.Order = p
		} else {
			m.Order = len(ids) + m.Order + 1
		}
	}
	s.normalize()
}

var pakRe = regexp.MustCompile(`(?i)^pak(\d\d)_dir\.vpk$`)

// IsPakName reports whether name is a file the loader may manage.
func IsPakName(name string) bool { return pakRe.MatchString(name) }

// ---- VPK files ----

var vpkChunkRe = regexp.MustCompile(`(?i)_\d{3}\.vpk$`)

const vpkMagic = 0x55aa1234

func isVPKHeader(b []byte) bool {
	return len(b) >= 4 && uint32(b[0])|uint32(b[1])<<8|uint32(b[2])<<16|uint32(b[3])<<24 == vpkMagic
}

// archiveKind sniffs the first bytes of a file.
func archiveKind(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")) || bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return "zip"
	case bytes.HasPrefix(head, []byte("7z\xbc\xaf\x27\x1c")):
		return "7z"
	case bytes.HasPrefix(head, []byte("Rar!")):
		return "rar"
	case isVPKHeader(head):
		return "vpk"
	}
	return ""
}

// ErrUnsupportedArchive is returned for archive formats we can't read.
var ErrUnsupportedArchive = errors.New("unsupported archive: extract it yourself and add the .vpk file")

const maxVPKSize = 4 << 30

// ExtractVPKs copies every single-file VPK in an archive (zip, rar, 7z) or a
// bare VPK into destDir. It returns the stored file names, sorted by their
// path inside the archive.
func ExtractVPKs(src, destDir, fallbackName string) ([]string, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 8)
	n, _ := io.ReadFull(f, head)
	f.Close()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	kind := archiveKind(head[:n])
	if kind == "vpk" {
		name := sanitizeVPKName(fallbackName)
		if err := fsutil.CopyFile(src, filepath.Join(destDir, name)); err != nil {
			return nil, err
		}
		return []string{name}, nil
	}
	if kind == "" {
		return nil, errors.New("not a .zip, .rar, .7z or .vpk file")
	}

	type found struct{ path, tmp string }
	var vpks []found
	var nested []string
	err = walkArchive(kind, src, func(name string, r io.Reader) error {
		name = strings.ReplaceAll(name, `\`, "/")
		base := filepath.Base(name)
		low := strings.ToLower(base)
		switch {
		case strings.HasPrefix(base, "."):
		case strings.HasSuffix(low, ".vpk"):
			if vpkChunkRe.MatchString(low) {
				return fmt.Errorf("%s is part of a multi-file VPK, which is not supported", base)
			}
			tmp := filepath.Join(destDir, fmt.Sprintf(".extract-%d.tmp", len(vpks)))
			if err := extractVPKTo(r, tmp, name); err != nil {
				return err
			}
			vpks = append(vpks, found{name, tmp})
		case strings.HasSuffix(low, ".7z") || strings.HasSuffix(low, ".rar") || strings.HasSuffix(low, ".zip"):
			nested = append(nested, base)
		}
		return nil
	})
	cleanup := func() {
		for _, v := range vpks {
			os.Remove(v.tmp)
		}
	}
	if err != nil {
		cleanup()
		return nil, err
	}
	if len(vpks) == 0 {
		if len(nested) > 0 {
			return nil, fmt.Errorf("no .vpk in the archive (it contains %s: extract it and add the .vpk)", strings.Join(nested, ", "))
		}
		return nil, errors.New("no .vpk file found in the archive")
	}
	sort.SliceStable(vpks, func(i, j int) bool { return strings.ToLower(vpks[i].path) < strings.ToLower(vpks[j].path) })
	var names []string
	used := map[string]bool{}
	for i, v := range vpks {
		name := sanitizeVPKName(filepath.Base(v.path))
		if used[strings.ToLower(name)] {
			name = fmt.Sprintf("%d_%s", i+1, name)
		}
		used[strings.ToLower(name)] = true
		if err := os.Rename(v.tmp, filepath.Join(destDir, name)); err != nil {
			cleanup()
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// walkArchive calls fn for every regular file in a zip, rar or 7z archive.
func walkArchive(kind, src string, fn func(name string, r io.Reader) error) error {
	switch kind {
	case "zip":
		zr, err := zip.OpenReader(src)
		if err != nil {
			return fmt.Errorf("bad zip: %w", err)
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			if err := openAndVisit(zf.Name, zf.Open, fn); err != nil {
				return err
			}
		}
		return nil
	case "7z":
		zr, err := sevenzip.OpenReader(src)
		if err != nil {
			return fmt.Errorf("bad 7z archive: %w", err)
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			if err := openAndVisit(zf.Name, zf.Open, fn); err != nil {
				return err
			}
		}
		return nil
	case "rar":
		rr, err := rardecode.OpenReader(src)
		if err != nil {
			return fmt.Errorf("bad rar archive: %w", err)
		}
		defer rr.Close()
		for {
			h, err := rr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("reading rar: %w", err)
			}
			if h.IsDir {
				continue
			}
			if err := fn(h.Name, rr); err != nil {
				return err
			}
		}
	}
	return ErrUnsupportedArchive
}

func openAndVisit(name string, open func() (io.ReadCloser, error), fn func(string, io.Reader) error) error {
	rc, err := open()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer rc.Close()
	return fn(name, rc)
}

func extractVPKTo(r io.Reader, dest, name string) error {
	head := make([]byte, 4)
	n, _ := io.ReadFull(r, head)
	if !isVPKHeader(head[:n]) {
		return fmt.Errorf("%s is not a valid VPK", name)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	out.Write(head[:n])
	if _, err := io.Copy(out, io.LimitReader(r, maxVPKSize)); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	return out.Close()
}

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func sanitizeVPKName(name string) string {
	name = unsafeNameRe.ReplaceAllString(filepath.Base(name), "_")
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "mod"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".vpk") {
		name += ".vpk"
	}
	return name
}

// ---- store / apply ----

// Store manages the on-disk mod store and applying it to the game.
type Store struct {
	Dir string // <data>/mods
}

func (st Store) ModDir(id string) string { return filepath.Join(st.Dir, id) }

// Desired returns the pak layout for enabled mods: pak01 = highest priority.
// With the main switch off (or during a repair) nothing is wanted.
func Desired(s *State) ([]AppliedPak, error) {
	var out []AppliedPak
	if s.ModsOff || s.Repair != nil {
		return nil, nil
	}
	for _, m := range s.Mods {
		if !m.Enabled {
			continue
		}
		for _, f := range m.Files {
			out = append(out, AppliedPak{Name: fmt.Sprintf("pak%02d_dir.vpk", len(out)+1), ModID: m.ID + "/" + f})
		}
	}
	if len(out) > 99 {
		return nil, fmt.Errorf("too many enabled VPKs (%d): the game supports pak01..pak99", len(out))
	}
	return out, nil
}

// UnknownPaks lists pakNN_dir.vpk files in addons that the loader didn't write
// (or that were changed since).
func UnknownPaks(addons string, applied []AppliedPak) ([]string, error) {
	ents, err := os.ReadDir(addons)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	known := map[string]AppliedPak{}
	for _, a := range applied {
		known[strings.ToLower(a.Name)] = a
	}
	var unknown []string
	for _, e := range ents {
		if e.IsDir() || !IsPakName(e.Name()) {
			continue
		}
		a, ok := known[strings.ToLower(e.Name())]
		if ok {
			info, err := e.Info()
			if err == nil && info.Size() == a.Size {
				if h, err := fsutil.CachedSHA256(filepath.Join(addons, e.Name()), info); err == nil && h == a.SHA256 {
					continue
				}
			}
		}
		unknown = append(unknown, e.Name())
	}
	sort.Strings(unknown)
	return unknown, nil
}

// UnknownPak describes a pakNN_dir.vpk the loader didn't write (or that was
// changed since), for the page: what it is and what it replaced.
type UnknownPak struct {
	Name     string      `json:"name"`
	Size     int64       `json:"size"`
	Modified time.Time   `json:"modified"`
	Replaces *Replaced   `json:"replaces,omitempty"` // the loader's mod that wrote a file with this name
	Contents *VPKSummary `json:"contents,omitempty"`
}

// Replaced is the loader mod a foreign file took the place of.
type Replaced struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// DescribeUnknown returns details for each foreign pak in addons: size, time,
// a summary of its contents, and the installed mod whose file it replaced
// (the loader last wrote a file with the same name for that mod).
func DescribeUnknown(addons string, s *State) ([]UnknownPak, error) {
	names, err := UnknownPaks(addons, s.Applied)
	if err != nil {
		return nil, err
	}
	wrote := map[string]string{}
	for _, a := range s.Applied {
		wrote[strings.ToLower(a.Name)] = a.ModID
	}
	out := []UnknownPak{}
	for _, n := range names {
		p := filepath.Join(addons, n)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		u := UnknownPak{Name: n, Size: info.Size(), Modified: info.ModTime()}
		if m := s.Mod(wrote[strings.ToLower(n)]); m != nil {
			u.Replaces = &Replaced{ID: m.ID, Name: m.Name, Version: m.Version}
		}
		if sum, err := SummarizeVPK(p); err == nil {
			u.Contents = sum
		}
		out = append(out, u)
	}
	return out, nil
}

// RestorePak writes back the file the loader last put at name (matched by
// its recorded hash in the mod's stored files). Used after deleting a foreign
// copy, so the loader's own version returns even while other foreign files
// still block a full apply. It reports whether anything was restored.
func (st Store) RestorePak(s *State, addons, name string) (bool, error) {
	for _, a := range s.Applied {
		if !strings.EqualFold(a.Name, name) || !IsPakName(a.Name) {
			continue
		}
		m := s.Mod(a.ModID)
		if m == nil || !m.Enabled {
			return false, nil
		}
		for _, f := range m.Files {
			src := filepath.Join(st.ModDir(m.ID), f)
			if h, err := fsutil.SHA256File(src); err == nil && h == a.SHA256 {
				return true, fsutil.CopyFileAtomic(src, filepath.Join(addons, a.Name))
			}
		}
	}
	return false, nil
}

// KeepPak takes over one foreign pak as a local mod. If it replaced one of the
// loader's mods, that mod is turned off (both would load and clash) and the
// kept file takes its place in the order. It returns the new mod.
func (st Store) KeepPak(s *State, addons, name string) (*Mod, error) {
	details, err := DescribeUnknown(addons, s)
	if err != nil {
		return nil, err
	}
	var d *UnknownPak
	for i := range details {
		if strings.EqualFold(details[i].Name, name) {
			d = &details[i]
		}
	}
	if d == nil {
		return nil, fmt.Errorf("%s isn't a file the loader didn't install", name)
	}
	added, err := st.ImportPaks(s, addons, []string{d.Name})
	if err != nil {
		return nil, err
	}
	if len(added) != 1 {
		return nil, fmt.Errorf("couldn't take over %s", name)
	}
	m := added[0]
	if d.Replaces != nil {
		if old := s.Mod(d.Replaces.ID); old != nil {
			old.Enabled = false
			m.Name = old.Name + " (your copy)"
			// Take its place in the order: same priority as the mod it replaced.
			var ids []string
			for _, x := range s.Mods {
				if x == m {
					continue
				}
				if x == old {
					ids = append(ids, m.ID)
				}
				ids = append(ids, x.ID)
			}
			s.SetOrder(ids)
		}
	}
	return m, nil
}

// ErrUnknownPaks blocks an apply until the user decides about foreign files.
type ErrUnknownPaks struct{ Files []string }

func (e *ErrUnknownPaks) Error() string {
	return "the addons folder has VPKs this loader didn't install: " + strings.Join(e.Files, ", ") + " (import them first)"
}

// Apply writes the enabled mods into addons as pak01_dir.vpk, pak02_dir.vpk...
// and removes paks it previously wrote that are no longer needed. It never
// touches files other than ^pakNN_dir.vpk$ and refuses if any of those are
// unknown.
func (st Store) Apply(s *State, addons string) error {
	if err := os.MkdirAll(addons, 0o755); err != nil {
		return err
	}
	unknown, err := UnknownPaks(addons, s.Applied)
	if err != nil {
		return err
	}
	if len(unknown) > 0 {
		return &ErrUnknownPaks{Files: unknown}
	}
	desired, err := Desired(s)
	if err != nil {
		return err
	}
	prev := map[string]AppliedPak{}
	for _, a := range s.Applied {
		prev[strings.ToLower(a.Name)] = a
	}
	var written []AppliedPak
	for i := range desired {
		d := &desired[i]
		// ModID is "<mod id>/<file>"; mod ids may contain one "/" (repository mods).
		cut := strings.LastIndex(d.ModID, "/")
		modID, file := d.ModID[:cut], d.ModID[cut+1:]
		src := filepath.Join(st.ModDir(modID), file)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("mod %s: %w", modID, err)
		}
		h, err := fsutil.SHA256File(src)
		if err != nil {
			return err
		}
		d.Size, d.SHA256, d.ModID = info.Size(), h, modID
		dst := filepath.Join(addons, d.Name)
		if p, ok := prev[strings.ToLower(d.Name)]; ok && p.SHA256 == h && p.Size == d.Size {
			// Already in place (UnknownPaks verified its hash) - unless the file
			// was deleted since, e.g. a foreign copy was removed: then write it.
			if fi, err := os.Stat(dst); err == nil && fi.Size() == d.Size {
				written = append(written, *d)
				continue
			}
		}
		fsutil.SetHashCache(dst, nil, "")
		if err := fsutil.CopyFileAtomic(src, dst); err != nil {
			s.Applied = mergeApplied(s.Applied, written)
			return fmt.Errorf("writing %s: %w", d.Name, err)
		}
		if info, err := os.Stat(dst); err == nil {
			fsutil.SetHashCache(dst, info, h)
		}
		written = append(written, *d)
	}
	keep := map[string]bool{}
	for _, d := range desired {
		keep[strings.ToLower(d.Name)] = true
	}
	for _, a := range s.Applied {
		if keep[strings.ToLower(a.Name)] || !IsPakName(a.Name) {
			continue
		}
		if err := os.Remove(filepath.Join(addons, a.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.Applied = mergeApplied(s.Applied, written)
			return fmt.Errorf("removing %s: %w", a.Name, err)
		}
	}
	s.Applied = written
	paks := map[string][]string{}
	for _, d := range desired {
		paks[d.ModID] = append(paks[d.ModID], d.Name)
	}
	for _, m := range s.Mods {
		m.Paks = paks[m.ID]
	}
	s.AppliedOnce = true
	s.PendingApply = false
	return nil
}

// RemoveOurPaks deletes the pak files the loader wrote (and that are still
// its own), leaving any other files alone. Unlike Apply it works while other
// programs' files are in the folder. The mods stay in the store.
func (st Store) RemoveOurPaks(s *State, addons string) (int, error) {
	unknown, err := UnknownPaks(addons, s.Applied)
	if err != nil {
		return 0, err
	}
	foreign := map[string]bool{}
	for _, u := range unknown {
		foreign[strings.ToLower(u)] = true
	}
	var left, failed []AppliedPak
	n := 0
	for _, a := range s.Applied {
		if foreign[strings.ToLower(a.Name)] || !IsPakName(a.Name) {
			// Replaced by someone else: not ours to delete. The record stays so
			// the page can still say which mod it replaced.
			left = append(left, a)
			continue
		}
		err := os.Remove(filepath.Join(addons, a.Name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			left = append(left, a)
			failed = append(failed, a)
			continue
		}
		if err == nil {
			n++
		}
	}
	s.Applied = left
	for _, m := range s.Mods {
		m.Paks = nil
	}
	s.PendingApply = true
	if len(failed) > 0 {
		return n, fmt.Errorf("couldn't remove %s", failed[0].Name)
	}
	return n, nil
}

// mergeApplied keeps the record consistent after a partial apply: entries in
// newer override older ones with the same name.
func mergeApplied(old, newer []AppliedPak) []AppliedPak {
	idx := map[string]int{}
	out := append([]AppliedPak(nil), old...)
	for i, a := range out {
		idx[strings.ToLower(a.Name)] = i
	}
	for _, a := range newer {
		if i, ok := idx[strings.ToLower(a.Name)]; ok {
			out[i] = a
		} else {
			out = append(out, a)
		}
	}
	return out
}

// ImportPaks moves foreign pakNN_dir.vpk files from addons into the store as
// local mods (enabled, in their current priority order, appended at the end).
func (st Store) ImportPaks(s *State, addons string, files []string) ([]*Mod, error) {
	sort.Strings(files)
	var added []*Mod
	for _, f := range files {
		if !IsPakName(f) {
			continue
		}
		id := NewLocalID("imported-" + strings.TrimSuffix(strings.ToLower(f), "_dir.vpk"))
		dir := st.ModDir(id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return added, err
		}
		if err := fsutil.MoveFile(filepath.Join(addons, f), filepath.Join(dir, f)); err != nil {
			os.RemoveAll(dir)
			return added, fmt.Errorf("importing %s: %w", f, err)
		}
		m := &Mod{ID: id, Source: SourceLocal, Name: "Imported " + f, Enabled: true, Files: []string{f}, InstalledAt: time.Now()}
		s.AddMod(m)
		added = append(added, m)
	}
	return added, nil
}

// ---- file helpers ----

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "mod"
	}
	return s
}

func NewLocalID(name string) string {
	return "local-" + Slugify(name) + "-" + fsutil.RandomHex(3)
}

// Repo is a user-added repository (state.json).
type Repo struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Key      string    `json:"key,omitempty"`      // DPAPI-protected, base64; never the plain key
	KeyLast4 string    `json:"keyLast4,omitempty"` // for "•••• abcd"
	AddedAt  time.Time `json:"addedAt"`
}
