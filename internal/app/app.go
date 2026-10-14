// Package app is the loader itself: the state, every operation the page and
// the tray offer (install, toggle, order, apply, play, repositories, links),
// the background loops (gameinfo watcher, update checks) and self-update.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gameinfo"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
)

// Version is the running loader's version (set by main from -ldflags).
var Version = "0.0.0-dev"

// ErrGameRunning is returned when the addons folder can't be written.
var ErrGameRunning = errors.New("Close Deadlock first: the game locks its mod files while running")

// ErrNoGame: the game folder isn't known yet (mods are kept and applied once it is).
var ErrNoGame = errors.New("game folder not set")

// App ties the state, the mod store and the game folder together.
type App struct {
	dataDir   string
	statePath string
	store     mods.Store

	mu         sync.Mutex // guards the fields below
	state      *mods.State
	catalog    *catalog.Catalog
	catalogErr string
	gbRemote   map[string]*gamebanana.Mod // key: gamebanana.Key(model, id)
	lastCheck  time.Time
	lastErr    string
	busy       string    // description of the running operation
	catalogAt  time.Time // last successful catalog fetch
	repoStates map[string]*repoState
	reposAt    time.Time // last refresh of third-party repositories
	events     []Event
	eventSeq   int

	opMu     sync.Mutex // serializes operations that change files
	checkNow chan struct{}
	quit     chan struct{}
	quitOnce sync.Once
	baseURL  string
	token    string

	// StopServer and StartServer let a self-update release the port before
	// starting the new exe (and take it back if that fails). Set by main.
	StopServer  func()
	StartServer func() error
	background  bool // started with --background (Windows startup)
}

func NewApp(dataDir string) (*App, error) {
	a := &App{
		dataDir:   dataDir,
		statePath: filepath.Join(dataDir, "state.json"),
		store:     mods.Store{Dir: filepath.Join(dataDir, "mods")},
		gbRemote:  map[string]*gamebanana.Mod{},
		checkNow:  make(chan struct{}, 1),
		quit:      make(chan struct{}),
	}
	st, err := mods.LoadState(a.statePath)
	if err != nil {
		log.Printf("state: %v (starting fresh, old file kept as state.json.broken)", err)
		os.Rename(a.statePath, a.statePath+".broken")
	}
	a.state = st
	if st.GameDir == "" || !steam.IsGameDir(st.GameDir) {
		if dir, err := steam.FindGame(); err == nil {
			log.Printf("found Deadlock at %s", dir)
			st.GameDir = dir
		} else {
			log.Printf("game detection: %v", err)
		}
	}
	return a, a.saveLocked()
}

func (a *App) saveLocked() error {
	if err := a.state.Save(a.statePath); err != nil {
		log.Printf("saving state: %v", err)
		return err
	}
	return nil
}

func (a *App) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastErr = ""
	} else {
		a.lastErr = err.Error()
	}
}

func (a *App) GameDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.GameDir
}

// beginOp takes the operation lock and records what is running for the UI.
func (a *App) beginOp(what string) func() {
	a.opMu.Lock()
	a.mu.Lock()
	a.busy = what
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		a.busy = ""
		a.mu.Unlock()
		a.opMu.Unlock()
	}
}

// SetGameDir validates and stores a user-chosen folder ("" = auto-detect).
func (a *App) SetGameDir(dir string) error {
	var root string
	if strings.TrimSpace(dir) == "" {
		d, err := steam.FindGame()
		if err != nil {
			return err
		}
		root = d
	} else if root = steam.NormalizeGameDir(dir); root == "" {
		return fmt.Errorf("%s is not a Deadlock folder (expected game\\citadel\\gameinfo.gi inside)", dir)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !strings.EqualFold(filepath.Clean(root), filepath.Clean(a.state.GameDir)) {
		a.state.Applied = nil // a different install: nothing there is ours
		a.state.AppliedOnce = false
	}
	a.state.GameDir = root
	log.Printf("game folder set to %s", root)
	return a.saveLocked()
}

// EnsureGameinfo re-adds the addons search path if a game update removed it.
func (a *App) EnsureGameinfo() (bool, error) {
	dir := a.GameDir()
	if dir == "" {
		return false, ErrNoGame
	}
	changed, err := gameinfo.Fix(dir)
	if changed {
		log.Printf("gameinfo.gi: re-added %s search path", gameinfo.AddonsPath)
	}
	return changed, err
}

// Apply writes enabled mods into the game (opMu must be held).
func (a *App) applyLocked() error {
	dir := a.GameDir()
	if dir == "" {
		return ErrNoGame
	}
	if platform.IsGameRunning() {
		a.mu.Lock()
		a.state.PendingApply = true
		a.saveLocked()
		a.mu.Unlock()
		return ErrGameRunning
	}
	if _, err := a.EnsureGameinfo(); err != nil {
		log.Printf("gameinfo: %v", err)
	}
	_, addons, _ := steam.Paths(dir)
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.store.Apply(a.state, addons)
	if err != nil {
		a.state.PendingApply = true
		log.Printf("apply: %v", err)
	} else {
		log.Printf("applied %d VPK(s) to %s", len(a.state.Applied), addons)
	}
	a.saveLocked()
	return err
}

func (a *App) Apply() error {
	defer a.beginOp("Applying mods")()
	return a.applyLocked()
}

// changed marks state dirty and applies right away when possible. "Game
// running" and "foreign paks" are not reported as errors: the change stays
// pending and the page shows a banner for each.
func (a *App) changedLocked() error {
	a.mu.Lock()
	a.state.PendingApply = true
	a.saveLocked()
	a.mu.Unlock()
	err := a.applyLocked()
	var unk *mods.ErrUnknownPaks
	if err == nil || errors.Is(err, ErrGameRunning) || errors.Is(err, ErrNoGame) || errors.As(err, &unk) {
		return nil
	}
	return err
}

// installFiles extracts VPKs from src into the store under m.ID (replacing any
// previous Version) and registers the mod. opMu must be held.
func (a *App) installFiles(m *mods.Mod, src, fallbackName string) error {
	if !catalog.ValidModID(m.ID) {
		return fmt.Errorf("invalid mod id %q", m.ID)
	}
	staging := a.store.ModDir(m.ID) + ".new"
	os.RemoveAll(staging)
	files, err := mods.ExtractVPKs(src, staging, fallbackName)
	if err != nil {
		os.RemoveAll(staging)
		return err
	}
	final := a.store.ModDir(m.ID)
	old := final + ".old"
	os.RemoveAll(old)
	if err := os.Rename(final, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.RemoveAll(staging)
		return err
	}
	if err := os.Rename(staging, final); err != nil {
		os.Rename(old, final)
		return err
	}
	os.RemoveAll(old)
	m.Files = files
	m.InstalledAt = time.Now()
	a.mu.Lock()
	isNew := a.state.Mod(m.ID) == nil
	if isNew {
		m.Enabled = true
	}
	a.state.AddMod(m)
	a.saveLocked()
	a.mu.Unlock()
	log.Printf("installed %s (%s) with %d VPK(s)", strings.TrimSpace(m.Name+" "+m.Version), m.ID, len(files))
	return nil
}

func (a *App) TempFile(pattern string) (string, error) {
	dir := filepath.Join(a.dataDir, "tmp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	return name, nil
}

// InstallCatalogMod downloads (or updates) one of our mods.
func (a *App) InstallCatalogMod(ctx context.Context, id string) error {
	defer a.beginOp("Installing " + id)()
	if err := a.installCatalogLocked(ctx, id); err != nil {
		return err
	}
	return a.changedLocked()
}

func (a *App) installCatalogLocked(ctx context.Context, id string) error {
	a.mu.Lock()
	cm := a.catalog.Mod(id)
	a.mu.Unlock()
	if cm == nil {
		return fmt.Errorf("%s is not in the catalog (check for updates first)", id)
	}
	tmp, err := a.TempFile("catalog-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := fetch.Download(ctx, cm.URL, tmp, fetch.Checksum{Algo: "sha256", Hex: cm.SHA256, Size: cm.Size}); err != nil {
		return err
	}
	return a.installFiles(&mods.Mod{ID: cm.ID, Source: mods.SourceCatalog, Name: cm.Name, Version: cm.Version, Page: cm.Page, Image: cm.Image}, tmp, cm.ID+".vpk")
}

// GameBananaInfo looks up a GameBanana item by link (the "add by link" box).
func (a *App) GameBananaInfo(ctx context.Context, link string) (*gamebanana.Mod, error) {
	model, id, err := gamebanana.ParseURL(link)
	if err != nil {
		return nil, err
	}
	return a.gbFetch(ctx, model, id)
}

func (a *App) gbFetch(ctx context.Context, model string, id int) (*gamebanana.Mod, error) {
	m, err := gamebanana.FetchMod(ctx, model, id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.gbRemote[gamebanana.Key(model, id)] = m
	// Back-fill the preview image of installs made before 0.3.0.
	if inst := a.state.Mod(gamebanana.ModID(model, id)); inst != nil &&
		((inst.Image == "" && m.Thumb() != "") || (inst.Category == "" && m.CategoryName() != "")) {
		if inst.Image == "" {
			inst.Image = m.Thumb()
		}
		if inst.Category == "" {
			inst.Category = m.CategoryName()
		}
		a.saveLocked()
	}
	a.mu.Unlock()
	return m, nil
}

// AddGameBanana installs a GameBanana item, given either a link or model+id;
// fileID 0 = newest file (kept up to date), another file pins the mod.
func (a *App) AddGameBanana(ctx context.Context, link, model string, id, fileID int) error {
	defer a.beginOp("Downloading from GameBanana")()
	var err error
	if link != "" {
		if model, id, err = gamebanana.ParseURL(link); err != nil {
			return err
		}
	}
	if id <= 0 {
		return errors.New("no GameBanana item given")
	}
	gb, err := a.gbFetch(ctx, model, id)
	if err != nil {
		return err
	}
	newest, ok := gb.Newest()
	if !ok {
		return errors.New("this GameBanana item has no files")
	}
	f := newest
	if fileID != 0 {
		if f, ok = gb.File(fileID); !ok {
			return fmt.Errorf("file %d not found on that item", fileID)
		}
	}
	if err := a.installGBLocked(ctx, gb, f, f.ID != newest.ID); err != nil {
		return err
	}
	return a.changedLocked()
}

func (a *App) installGBLocked(ctx context.Context, gb *gamebanana.Mod, f gamebanana.File, pinned bool) error {
	if f.AvResult != "" && f.AvResult != "clean" {
		return fmt.Errorf("GameBanana flagged %s (%s); not installing", f.Name, f.AvResult)
	}
	tmp, err := a.TempFile("gb-*.download")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	sum := fetch.Checksum{Algo: "md5", Hex: f.MD5, Size: f.Size}
	if f.MD5 == "" {
		sum = fetch.Checksum{Algo: "none"}
	}
	if err := fetch.Download(ctx, f.URL, tmp, sum); err != nil {
		return err
	}
	m := &mods.Mod{ID: gamebanana.ModID(gb.Model, gb.ID), Source: mods.SourceGameBanana, Name: gb.Name, Version: gb.Version, Page: gb.ProfileURL,
		Image: gb.Thumb(), Category: gb.CategoryName(), GBID: gb.ID, GBModel: gb.Model, GBFileID: f.ID, GBFileName: f.Name, GBModified: gb.DateModified, GBPinned: pinned}
	if err := a.installFiles(m, tmp, f.Name); err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	return nil
}

// AddLocal installs an uploaded .zip/.vpk.
func (a *App) AddLocal(src, filename string) error {
	defer a.beginOp("Adding " + filename)()
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)), "_dir")
	m := &mods.Mod{ID: mods.NewLocalID(name), Source: mods.SourceLocal, Name: name}
	if err := a.installFiles(m, src, filename); err != nil {
		return err
	}
	return a.changedLocked()
}

func (a *App) SetEnabled(id string, on bool) error {
	defer a.beginOp("Updating mods")()
	a.mu.Lock()
	m := a.state.Mod(id)
	if m == nil {
		a.mu.Unlock()
		return errors.New("no such mod")
	}
	m.Enabled = on
	a.mu.Unlock()
	return a.changedLocked()
}

func (a *App) SetOrder(ids []string) error {
	defer a.beginOp("Reordering mods")()
	a.mu.Lock()
	a.state.SetOrder(ids)
	a.mu.Unlock()
	return a.changedLocked()
}

// Remove deletes a mod from the store. Its paks leave the game on the next apply.
func (a *App) Remove(id string) error {
	defer a.beginOp("Removing mod")()
	a.mu.Lock()
	m := a.state.Mod(id)
	if m == nil {
		a.mu.Unlock()
		return errors.New("no such mod")
	}
	a.state.RemoveMod(id)
	a.saveLocked()
	a.mu.Unlock()
	if catalog.ValidModID(id) {
		os.RemoveAll(a.store.ModDir(id))
	}
	log.Printf("removed %s", m.Name)
	return a.changedLocked()
}

// ImportUnknown moves foreign pakNN_dir.vpk files into the store as local mods.
func (a *App) ImportUnknown() error {
	defer a.beginOp("Importing existing mods")()
	dir := a.GameDir()
	if dir == "" {
		return ErrNoGame
	}
	if platform.IsGameRunning() {
		return ErrGameRunning
	}
	_, addons, _ := steam.Paths(dir)
	a.mu.Lock()
	unknown, err := mods.UnknownPaks(addons, a.state.Applied)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	added, err := a.store.ImportPaks(a.state, addons, unknown)
	a.saveLocked()
	a.mu.Unlock()
	log.Printf("imported %d existing VPK(s)", len(added))
	if err != nil {
		return err
	}
	return a.changedLocked()
}

// Play makes sure everything is in place, then launches through Steam.
func (a *App) Play(force bool) error {
	if !platform.IsGameRunning() {
		if _, err := a.EnsureGameinfo(); err != nil && !force {
			return err
		}
		a.mu.Lock()
		pending := a.state.PendingApply || !a.state.AppliedOnce
		needed := len(a.state.Mods) > 0 || a.state.AppliedOnce
		a.mu.Unlock()
		if pending && needed {
			if err := a.Apply(); err != nil && !force {
				return err
			}
		}
	}
	log.Printf("launching Deadlock")
	return platform.OpenURL("steam://rungameid/" + steam.AppID)
}

// SetSettings stores settings and applies the start-with-Windows toggle.
func (a *App) SetSettings(s mods.Settings) error {
	a.mu.Lock()
	old := a.state.Settings
	s.DontAsk = old.DontAsk // changed only through SetDontAsk / ResetDontAsk
	a.state.Settings = s
	err := a.saveLocked()
	a.mu.Unlock()
	if old.StartWithWindows != s.StartWithWindows {
		if e := applyRunKey(s.StartWithWindows); e != nil {
			return e
		}
	}
	return err
}

// applyRunKey registers or removes the HKCU Run entry for this exe.
func applyRunKey(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := platform.SetRunAtStartup(on, fmt.Sprintf(`"%s" --background`, exe)); err != nil {
		log.Printf("start with Windows: %v", err)
		return err
	}
	return nil
}

// ShowWindow opens the loader window or brings it to the front, and refreshes
// the Frogtech catalog if it is missing or older than 5 minutes.
func (a *App) ShowWindow() {
	winui.ShowWindow(a)
	a.refreshCatalogIfStale()
}

// ShowWindowAt opens the window on a section ("updates" = Settings > Updates).
func (a *App) ShowWindowAt(nav string) {
	if nav != "" {
		a.pushEvent(Event{Kind: "nav", Text: nav})
	}
	a.ShowWindow()
}

func (a *App) refreshCatalogIfStale() {
	a.mu.Lock()
	stale := a.catalog == nil || time.Since(a.catalogAt) > 5*time.Minute
	reposStale := time.Since(a.reposAt) > 5*time.Minute
	if reposStale {
		a.reposAt = time.Now()
	}
	a.mu.Unlock()
	if stale {
		go func() {
			if err := a.RefreshCatalog(context.Background()); err != nil {
				log.Printf("catalog: %v", err)
			}
		}()
	}
	if reposStale {
		go a.RefreshRepos(context.Background())
	}
}

// KeepInTray reports whether closing the window should leave the loader
// running in the tray.
func (a *App) KeepInTray() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.background || a.state.Settings.StartWithWindows
}

func (a *App) WindowRect() *mods.WindowRect {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.Window
}

func (a *App) SetWindowRect(r *mods.WindowRect) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Window = r
	a.saveLocked()
}

// Quit stops the process (background mode included).
func (a *App) Quit() {
	a.quitOnce.Do(func() {
		log.Printf("quitting")
		close(a.quit)
		winui.QuitUI()
	})
}

// BulkMods enables, disables or removes several mods with a single apply.
func (a *App) BulkMods(ids []string, action string) (int, error) {
	if action != "enable" && action != "disable" && action != "remove" {
		return 0, fmt.Errorf("unknown bulk action %q", action)
	}
	defer a.beginOp("Updating " + strconv.Itoa(len(ids)) + " mods")()
	n := 0
	var removed []string
	a.mu.Lock()
	for _, id := range ids {
		m := a.state.Mod(id)
		if m == nil {
			continue
		}
		n++
		switch action {
		case "enable", "disable":
			m.Enabled = action == "enable"
		case "remove":
			a.state.RemoveMod(id)
			removed = append(removed, id)
		}
	}
	a.saveLocked()
	a.mu.Unlock()
	for _, id := range removed {
		if catalog.ValidModID(id) {
			os.RemoveAll(a.store.ModDir(id))
		}
	}
	log.Printf("bulk %s: %d mod(s)", action, n)
	return n, a.changedLocked()
}

// DeleteForeign deletes pakNN_dir.vpk files in addons that the loader didn't
// write (only after the user confirmed in the page).
func (a *App) DeleteForeign() (int, error) {
	return a.deleteForeign("")
}

// DeleteForeignFile deletes one foreign pak ("put back" in the page: the
// loader then writes its own file again).
func (a *App) DeleteForeignFile(name string) error {
	_, err := a.deleteForeign(name)
	return err
}

func (a *App) deleteForeign(only string) (int, error) {
	defer a.beginOp("Deleting foreign paks")()
	dir := a.GameDir()
	if dir == "" {
		return 0, ErrNoGame
	}
	if platform.IsGameRunning() {
		return 0, ErrGameRunning
	}
	_, addons, _ := steam.Paths(dir)
	a.mu.Lock()
	unknown, err := mods.UnknownPaks(addons, a.state.Applied)
	a.mu.Unlock()
	if err != nil {
		return 0, err
	}
	var deleted []string
	for _, f := range unknown {
		if !mods.IsPakName(f) || (only != "" && !strings.EqualFold(f, only)) {
			continue
		}
		if err := os.Remove(filepath.Join(addons, f)); err != nil {
			return len(deleted), err
		}
		deleted = append(deleted, f)
		// Put the loader's own file back right away (a full apply may still be
		// blocked by other foreign files).
		a.mu.Lock()
		restored, err := a.store.RestorePak(a.state, addons, f)
		a.mu.Unlock()
		if err != nil {
			log.Printf("putting back %s: %v", f, err)
		} else if restored {
			log.Printf("put back the loader's %s", f)
		}
	}
	if only != "" && len(deleted) == 0 {
		return 0, fmt.Errorf("%s isn't a file the loader didn't install", only)
	}
	log.Printf("deleted %d foreign pak(s): %s", len(deleted), strings.Join(deleted, ", "))
	return len(deleted), a.changedLocked()
}

// KeepForeign takes over one foreign pak as a mod of its own. If it replaced
// one of the loader's mods, that mod is turned off so the two don't clash.
func (a *App) KeepForeign(name string) (*mods.Mod, error) {
	defer a.beginOp("Keeping " + name)()
	dir := a.GameDir()
	if dir == "" {
		return nil, ErrNoGame
	}
	if platform.IsGameRunning() {
		return nil, ErrGameRunning
	}
	_, addons, _ := steam.Paths(dir)
	a.mu.Lock()
	m, err := a.store.KeepPak(a.state, addons, name)
	a.saveLocked()
	var kept mods.Mod
	if m != nil {
		kept = *m
	}
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	log.Printf("kept %s as %s", name, kept.Name)
	return &kept, a.changedLocked()
}

// SetDontAsk remembers (or forgets) "don't ask again" for a question key.
func (a *App) SetDontAsk(key string, on bool) error {
	if key == "" || len(key) > 40 {
		return errors.New("bad question key")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := &a.state.Settings
	if s.DontAsk == nil {
		s.DontAsk = map[string]bool{}
	}
	if on {
		s.DontAsk[key] = true
	} else {
		delete(s.DontAsk, key)
	}
	return a.saveLocked()
}

// ResetDontAsk makes every question ask again.
func (a *App) ResetDontAsk() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Settings.DontAsk = nil
	return a.saveLocked()
}

// DataDir is %LOCALAPPDATA%\FrogLoader.
func (a *App) DataDir() string { return a.dataDir }

// Done is closed when the app quits.
func (a *App) Done() <-chan struct{} { return a.quit }

// SetBaseURL records the local server's address (set by the server).
func (a *App) SetBaseURL(u string) { a.baseURL = u }

// Token is the local API token.
func (a *App) Token() string { return a.token }

// SetToken and SetBackground are set once by main.
func (a *App) SetToken(t string)     { a.token = t }
func (a *App) SetBackground(bg bool) { a.background = bg }

// RepoAdded reports whether a repository id is in the user's list.
func (a *App) RepoAdded(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.repoByID(id) != nil
}

// RemoveDMMLeftover deletes Deadlock Mod Manager's empty .dmm.json.
func (a *App) RemoveDMMLeftover() error {
	dir := a.GameDir()
	if dir == "" {
		return ErrNoGame
	}
	_, addons, _ := steam.Paths(dir)
	return mods.RemoveDMMLeftover(addons)
}

// StartSelfUpdate begins installing the newer loader (the page asked for it);
// a failure shows up as a toast and the current exe keeps running.
func (a *App) StartSelfUpdate() error {
	c := a.LoaderUpdate()
	if c == nil {
		return errors.New("no loader update available")
	}
	if platform.IsGameRunning() {
		return ErrGameRunning
	}
	go func() {
		if err := a.SelfUpdate(context.Background(), c); err != nil {
			log.Printf("self-update: %v", err)
			a.toastEvent("error", "Couldn't update the loader: "+err.Error()+". You're still on "+Version+".")
		}
	}()
	return nil
}

// InitRunKey applies the start-with-Windows default once and keeps the
// registered path pointing at the current exe.
func (a *App) InitRunKey() {
	a.mu.Lock()
	first := !a.state.RunKeyInit
	on := a.state.Settings.StartWithWindows
	a.state.RunKeyInit = true
	a.saveLocked()
	a.mu.Unlock()
	if first || on {
		applyRunKey(on)
	}
}
