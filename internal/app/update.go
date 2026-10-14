package app

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
)

const (
	gameinfoInterval = 5 * time.Second
	updateInterval   = 60 * time.Minute
	retryInterval    = 5 * time.Minute
)

// RunBackground runs the gameinfo watcher and the update checker until quit.
func (a *App) RunBackground() {
	go a.watchGameinfo()
	go a.updateLoop()
}

// watchGameinfo re-adds the addons line every few seconds (game updates reset
// gameinfo.gi) and applies pending mod changes once the game is closed.
func (a *App) watchGameinfo() {
	t := time.NewTicker(gameinfoInterval)
	defer t.Stop()
	for {
		a.mu.Lock()
		keep := a.state.Settings.KeepGameinfo && a.state.GameDir != ""
		pending := a.state.PendingApply && a.state.AppliedOnce
		a.mu.Unlock()
		if keep {
			if _, err := a.EnsureGameinfo(); err != nil {
				log.Printf("gameinfo: %v", err)
			}
		}
		if pending && !platform.IsGameRunning() && !a.hasUnknownPaks() && a.opMu.TryLock() {
			if err := a.applyLocked(); err != nil {
				a.setErr(err)
			}
			a.opMu.Unlock()
		}
		select {
		case <-a.quit:
			return
		case <-t.C:
		}
	}
}

// hasUnknownPaks avoids retrying an apply every few seconds while the user
// hasn't decided about foreign files (the page shows the import banner).
func (a *App) hasUnknownPaks() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.GameDir == "" {
		return false
	}
	_, addons, _ := steam.Paths(a.state.GameDir)
	u, err := mods.UnknownPaks(addons, a.state.Applied)
	return err != nil || len(u) > 0
}

func (a *App) updateLoop() {
	next := time.Duration(0) // check right away on start
	backoff := time.Minute
	for {
		select {
		case <-a.quit:
			return
		case <-time.After(next):
		case <-a.checkNow:
		}
		next = updateInterval
		res, err := a.CheckUpdates(context.Background(), true)
		if err != nil {
			log.Printf("update check: %v", err)
			if errors.Is(err, ErrGameRunning) {
				next = retryInterval
			}
		}
		a.reportCheck(res, err, false)
		// At Windows startup the network is often not up yet: retry the
		// catalog soon (1, 2, 4 … 15 min) instead of waiting an hour.
		a.mu.Lock()
		noCatalog := a.catalog == nil
		a.mu.Unlock()
		if noCatalog {
			next, backoff = backoff, min(backoff*2, 15*time.Minute)
		} else {
			backoff = time.Minute
		}
	}
}

// TriggerCheck asks the update loop to run now.
func (a *App) TriggerCheck() {
	select {
	case a.checkNow <- struct{}{}:
	default:
	}
}

// pendingUpdate is an available mod update.
type pendingUpdate struct {
	modID   string
	label   string
	install func(ctx context.Context) error
}

// notifyHook shows a desktop notification (replaced in tests).
var notifyHook = winui.Notify

// CheckResult says what an update check found and did.
type CheckResult struct {
	Current     string   `json:"current"`               // running loader Version
	Loader      string   `json:"loader,omitempty"`      // newer loader Version available
	LoaderNotes []string `json:"loaderNotes,omitempty"` // its "what's new"
	Mods        []string `json:"mods"`                  // mod updates available (not installed)
	Installed   []string `json:"installed"`             // mod updates installed by this check
	Waiting     bool     `json:"waiting"`               // updates wait for Deadlock to close
	RepoErrors  []string `json:"repoErrors,omitempty"`  // repositories that couldn't be checked
}

// CheckUpdates refreshes the catalog and GameBanana info. With install=true and
// auto-update on, it installs mod and loader updates when the game is closed
// (a loader update restarts the process).
func (a *App) CheckUpdates(ctx context.Context, install bool) (CheckResult, error) {
	res := CheckResult{Current: Version, Mods: []string{}, Installed: []string{}}
	catErr := a.RefreshCatalog(ctx)
	res.RepoErrors = a.RefreshRepos(ctx) // a failing repository never blocks the others
	a.mu.Lock()
	a.lastCheck, a.reposAt = time.Now(), time.Now()
	type gbRef struct {
		model string
		id    int
	}
	var gbIDs []gbRef
	for _, m := range a.state.Mods {
		if m.Source == mods.SourceGameBanana && m.GBID > 0 {
			gbIDs = append(gbIDs, gbRef{gamebanana.NormModel(m.GBModel), m.GBID})
		}
	}
	auto := a.state.Settings.AutoUpdate
	a.mu.Unlock()
	if catErr != nil {
		log.Printf("catalog: %v", catErr)
	}
	for _, r := range gbIDs {
		if _, err := a.gbFetch(ctx, r.model, r.id); err != nil {
			log.Printf("GameBanana %s %d: %v", r.model, r.id, err)
		}
	}
	ups := a.pendingUpdates()
	for _, u := range ups {
		res.Mods = append(res.Mods, u.label)
	}
	selfUp := a.LoaderUpdate()
	if selfUp != nil {
		res.Loader, res.LoaderNotes = selfUp.Loader.Version, selfUp.Loader.Notes
	}
	if catErr != nil && len(ups) == 0 {
		return res, fmt.Errorf("couldn't reach mods.frogtech.dev: %w", catErr)
	}
	if !install || !auto || (len(ups) == 0 && selfUp == nil) {
		return res, nil
	}
	if platform.IsGameRunning() {
		res.Waiting = true
		return res, fmt.Errorf("%d update(s) waiting: %w", len(ups)+boolInt(selfUp != nil), ErrGameRunning)
	}
	installed, err := a.installUpdates(ctx, ups, selfUp)
	res.Installed = installed
	res.Mods = []string{}
	for _, u := range ups {
		if !contains(installed, u.label) {
			res.Mods = append(res.Mods, u.label)
		}
	}
	return res, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// installUpdates installs mod updates (one apply) and then the loader
// update, which restarts the process. It returns the labels installed.
func (a *App) installUpdates(ctx context.Context, ups []pendingUpdate, selfUp *catalog.Catalog) ([]string, error) {
	done := a.beginOp("Installing updates")
	var errs []error
	installed := []string{}
	for _, u := range ups {
		log.Printf("updating %s", u.label)
		if err := u.install(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u.label, err))
			continue
		}
		installed = append(installed, u.label)
	}
	if len(ups) > 0 {
		if err := a.changedLocked(); err != nil {
			errs = append(errs, err)
		}
	}
	done()
	if selfUp != nil {
		notifyHook("Updating Frogtech Mod Loader", "Installing Version "+selfUp.Loader.Version+"; the loader restarts.", "")
		if err := a.SelfUpdate(ctx, selfUp); err != nil {
			errs = append(errs, fmt.Errorf("loader update: %w", err))
			a.toastEvent("error", "Couldn't update the loader: "+err.Error()+". You're still on "+Version+".")
		}
	}
	err := errors.Join(errs...)
	a.setErr(err)
	return installed, err
}

// UpdateAll installs every available mod update now (not the loader).
func (a *App) UpdateAll(ctx context.Context) ([]string, error) {
	if platform.IsGameRunning() {
		return nil, ErrGameRunning
	}
	return a.installUpdates(ctx, a.pendingUpdates(), nil)
}

// reportCheck tells the user what a check found. manual=true is a check the
// user asked for from the tray: it always reports. Background checks only
// speak when they installed something or a new loader Version waits.
func (a *App) reportCheck(res CheckResult, err error, manual bool) {
	switch {
	case err != nil && !errors.Is(err, ErrGameRunning):
		if manual {
			notifyHook("Couldn't check for updates", err.Error()+".", "")
		}
		if len(res.Installed) == 0 {
			return
		}
	}
	var parts []string
	if len(res.Installed) > 0 {
		notifyHook("Mods updated", "Updated: "+strings.Join(res.Installed, ", ")+".", "updates")
		a.toastEvent("ok", "Updated: "+strings.Join(res.Installed, ", "))
	}
	if res.Loader != "" {
		parts = append(parts, "Loader "+res.Loader)
	}
	parts = append(parts, res.Mods...)
	if len(parts) > 0 {
		tail := "Open the loader to install."
		if res.Waiting {
			tail = "They install when you close Deadlock."
		}
		a.mu.Lock()
		announced := a.state.NotifiedLoader == res.Loader
		auto := a.state.Settings.AutoUpdate
		if res.Loader != "" && !announced {
			a.state.NotifiedLoader = res.Loader
			a.saveLocked()
		}
		a.mu.Unlock()
		// Background: one notification per new loader Version (auto-update off);
		// waiting mod updates are only announced when asked.
		if manual || (res.Loader != "" && !announced && !auto) {
			notifyHook("Updates found", strings.Join(parts, ", ")+". "+tail, "updates")
		}
		return
	}
	if manual && len(res.Installed) == 0 {
		msg := "Frogtech Mod Loader " + Version + ", all mods current."
		if len(res.RepoErrors) > 0 {
			msg += " Couldn't check " + strings.Join(res.RepoErrors, "; ") + "."
		}
		notifyHook("You're up to date", msg, "")
	}
}

// CheckFromTray is the tray's "Check for updates": install if auto-update is
// on, and always report the result.
func (a *App) CheckFromTray() {
	a.mu.Lock()
	auto := a.state.Settings.AutoUpdate
	a.mu.Unlock()
	res, err := a.CheckUpdates(context.Background(), auto)
	if err != nil {
		log.Printf("update check: %v", err)
	}
	a.reportCheck(res, err, true)
}

// UpdateMod installs the available update for one mod now.
func (a *App) UpdateMod(ctx context.Context, id string) error {
	for _, u := range a.pendingUpdates() {
		if u.modID == id {
			defer a.beginOp("Updating " + u.label)()
			if err := u.install(ctx); err != nil {
				return err
			}
			return a.changedLocked()
		}
	}
	return errors.New("no update available for that mod")
}

// pendingUpdates lists installed mods with a newer Version available.
// The install funcs expect opMu to be held.
func (a *App) pendingUpdates() []pendingUpdate {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []pendingUpdate
	for _, m := range a.state.Mods {
		switch m.Source {
		case mods.SourceRepo:
			r, cm, _ := a.repoMod(m.Repo, m.RepoMod)
			if r != nil && r.Enabled && cm != nil && catalog.CompareVersions(cm.Version, m.Version) > 0 {
				repo, mod := m.Repo, m.RepoMod
				out = append(out, pendingUpdate{modID: m.ID, label: cm.Name + " " + cm.Version,
					install: func(ctx context.Context) error { return a.installRepoLocked(ctx, repo, mod) }})
			}
		case mods.SourceCatalog:
			if a.state.FrogtechDisabled {
				continue
			}
			cm := a.catalog.Mod(m.ID)
			if cm != nil && catalog.CompareVersions(cm.Version, m.Version) > 0 {
				id := m.ID
				out = append(out, pendingUpdate{modID: id, label: cm.Name + " " + cm.Version,
					install: func(ctx context.Context) error { return a.installCatalogLocked(ctx, id) }})
			}
		case mods.SourceGameBanana:
			gb := a.gbRemote[gamebanana.Key(m.GBModel, m.GBID)]
			if gb == nil {
				continue
			}
			if f, ok := gamebanana.UpdateAvailable(m.GBFileID, m.GBPinned, gb); ok {
				out = append(out, pendingUpdate{modID: m.ID, label: gb.Name + " (" + f.Name + ")",
					install: func(ctx context.Context) error { return a.installGBLocked(ctx, gb, f, false) }})
			}
		}
	}
	return out
}

// loaderUpdate returns the catalog if it offers a newer loader.
func (a *App) LoaderUpdate() *catalog.Catalog {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.catalog
	if c == nil || c.Loader.Version == "" || c.Loader.URL == "" || c.Loader.SHA256 == "" {
		return nil
	}
	if catalog.CompareVersions(c.Loader.Version, Version) <= 0 {
		return nil
	}
	return c
}

// SelfUpdate replaces the running exe with the catalog's build and restarts.
// catalog.loader.url points to a zip containing FrogLoader.exe (a bare exe is
// accepted too). Steps: download next to the exe as FrogLoader.update.zip,
// verify sha256, extract FrogLoader.exe to <exe>.new, rename <exe> -> <exe>.old,
// <exe>.new -> <exe>, start it, exit. <exe>.old is deleted on the next start.
func (a *App) SelfUpdate(ctx context.Context, c *catalog.Catalog) error {
	defer a.beginOp("Updating the loader")()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	pkg := filepath.Join(dir, "FrogLoader.update.zip")
	newPath, oldPath := exe+".new", exe+".old"
	defer os.Remove(pkg)
	if err := fetch.Download(ctx, c.Loader.URL, pkg, fetch.Checksum{Algo: "sha256", Hex: c.Loader.SHA256}); err != nil {
		return err
	}
	if err := extractLoaderExe(pkg, newPath); err != nil {
		os.Remove(newPath)
		return err
	}
	os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Rename(newPath, exe); err != nil {
		os.Rename(oldPath, exe)
		return err
	}
	log.Printf("loader updated %s -> %s, restarting", Version, c.Loader.Version)
	if err := a.restart(exe); err != nil {
		// Couldn't start the new exe: put the old one back and keep running.
		os.Rename(exe, newPath)
		os.Rename(oldPath, exe)
		os.Remove(newPath)
		return err
	}
	return nil
}

// extractLoaderExe writes FrogLoader.exe from the update package to dest.
func extractLoaderExe(pkg, dest string) error {
	f, err := os.Open(pkg)
	if err != nil {
		return err
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	f.Close()
	if n >= 2 && string(head[:2]) == "MZ" { // a bare exe (older catalogs)
		return fsutil.CopyFile(pkg, dest)
	}
	zr, err := zip.OpenReader(pkg)
	if err != nil {
		return fmt.Errorf("update package: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if !strings.EqualFold(filepath.Base(strings.ReplaceAll(zf.Name, `\`, "/")), "FrogLoader.exe") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		mz := make([]byte, 2)
		if _, err := io.ReadFull(rc, mz); err != nil || string(mz) != "MZ" {
			out.Close()
			return errors.New("update package: FrogLoader.exe is not a Windows program")
		}
		out.Write(mz)
		if _, err := io.Copy(out, io.LimitReader(rc, 256<<20)); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return errors.New("update package has no FrogLoader.exe")
}

// restart releases the port, starts the new exe and exits. The new process
// reopens the window if it was open, otherwise it starts in the background.
func (a *App) restart(exe string) error {
	args := []string{}
	if !winui.WindowOpen() {
		args = append(args, "--background")
	}
	if a.StopServer != nil {
		a.StopServer()
	}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		if a.StartServer != nil {
			if serr := a.StartServer(); serr != nil {
				log.Printf("restarting server: %v", serr)
			}
		}
		return fmt.Errorf("relaunch: %w", err)
	}
	a.Quit()
	go func() { time.Sleep(2 * time.Second); os.Exit(0) }()
	return nil
}

// CleanupOldExe removes the previous exe left behind by a self-update.
func CleanupOldExe() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	for i := 0; i < 5; i++ {
		if err := os.Remove(exe + ".old"); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(time.Second)
	}
}
