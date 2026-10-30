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
	"github.com/fr0gtech/frogtech-mod-loader/internal/gameinfo"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
)

const (
	gameinfoInterval = 5 * time.Second
	updateInterval   = 60 * time.Minute
	retryInterval    = 5 * time.Minute
)

// RunBackground runs the gameinfo watcher, the update checker and the match
// alert until quit.
func (a *App) RunBackground() {
	go a.watchGameinfo()
	go a.updateLoop()
	go a.watchQueue()
}

// watchGameinfo re-adds the addons line every few seconds (game updates reset
// gameinfo.gi) and applies pending mod changes once the game is closed.
func (a *App) watchGameinfo() {
	t := time.NewTicker(gameinfoInterval)
	defer t.Stop()
	var seenGame string
	for {
		a.gameinfoTick(&seenGame)
		select {
		case <-a.quit:
			return
		case <-t.C:
		}
	}
}

// gameinfoTick is one round of watchGameinfo. seenGame is the game's
// ClientVersion at the last round: when Steam updated the game, stale mods
// are parked before the addons line goes back into gameinfo.gi.
func (a *App) gameinfoTick(seenGame *string) {
	if !a.parkOnGameChange(seenGame) {
		return // busy: try again next round, gameinfo.gi untouched until parked
	}
	a.mu.Lock()
	// Mods off and "Start without mods" keep Valve's file even without the setting.
	keep := (a.state.Settings.KeepGameinfo || a.modsOffLocked()) && a.state.GameDir != ""
	pending := a.state.PendingApply && a.state.AppliedOnce && a.state.Repair == nil
	a.mu.Unlock()
	a.watchStart()
	if !keep && a.halfPatched() {
		keep = true // a file with the addons line but no Mod/Write lines stops the game from starting
	}
	if keep {
		if _, err := a.EnsureGameinfo(); err != nil {
			log.Printf("gameinfo: %v", err)
		}
	}
	if pending && !gameRunning() && !a.hasUnknownPaks() && a.opMu.TryLock() {
		if err := a.applyLocked(); err != nil {
			a.setErr(err)
		}
		a.opMu.Unlock()
	}
}

// parkOnGameChange parks stale mods (parkStaleLocked) the first time it sees
// a ClientVersion, i.e. at start and right after a Steam update, and asks the
// update loop to look for builds. It returns false when another operation
// holds opMu, so the caller leaves gameinfo.gi alone this round.
func (a *App) parkOnGameChange(seenGame *string) bool {
	cv := a.clientVersion()
	if cv == "" || cv == *seenGame {
		return true
	}
	if !a.opMu.TryLock() {
		return false
	}
	jobs, err := a.parkStaleLocked(cv)
	a.opMu.Unlock()
	if err != nil {
		log.Printf("game builds: %v", err)
	}
	if *seenGame != "" {
		log.Printf("game updated: %s -> %s", *seenGame, cv)
	}
	*seenGame = cv
	if len(jobs) > 0 {
		a.TriggerCheck() // fetch the builds for the new game
	}
	return true
}

// halfPatched: gameinfo.gi mounts the mods folder without the Mod/Write lines
// (what loader 0.6 and older wrote).
func (a *App) halfPatched() bool {
	dir := a.GameDir()
	if dir == "" {
		return false
	}
	r, err := gameinfo.Status(dir)
	return err == nil && r.Addons && !r.Patched()
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
	// At start, before the catalog: a game update makes stale UI Kit files
	// placeholders (the builds.json URL is in state.json).
	if err := a.checkGameBuilds(context.Background()); err != nil {
		log.Printf("game builds: %v", err)
	}
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
		if err := a.checkGameBuilds(context.Background()); err != nil {
			log.Printf("game builds: %v", err)
		}
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
	if gameRunning() {
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
	if gameRunning() {
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
			if cm != nil && cm.Builds != "" && stale(cm.Game, a.clientVersionLocked()) {
				continue // the catalog's build is for another game build: checkGameBuilds
			}
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
	if err := fetch.Download(ctx, updateURL(c.Loader.URL), pkg, fetch.Checksum{Algo: "sha256", Hex: c.Loader.SHA256}); err != nil {
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

// ---- game builds ----
//
// UI Kit's paks carry copies of the game's layouts: after a Steam update they
// are stale and the game crashes at start. A catalog mod whose entry names a
// builds.json is parked when the game's ClientVersion differs from the one
// its files were built for (each of its paks becomes a placeholder, so the
// pak numbers of later mods don't move), until builds.json lists a published
// build for this game; that is installed and the mod unparked.

// stale: files built for game build `built` don't fit the game on `local`.
// An unknown build (installed before the loader recorded it) or an unknown
// game is never stale.
func stale(built, local string) bool {
	return built != "" && local != "" && built != local
}

// clientVersion is the game's ClientVersion from steam.inf ("" = unknown).
func (a *App) clientVersion() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clientVersionLocked()
}

func (a *App) clientVersionLocked() string {
	if a.state.GameDir == "" {
		return ""
	}
	return steam.ReadGameInfo(a.state.GameDir).ClientVersion
}

// checkGameBuilds parks and unparks catalog mods with a builds.json for the
// game's current build, and installs the build for it when one is listed.
// It runs at start (no catalog needed: the URL is in state.json) and after
// each update check.
func (a *App) checkGameBuilds(ctx context.Context) error {
	local := a.clientVersion()
	if local == "" {
		return nil
	}
	a.opMu.Lock()
	jobs, err := a.parkStaleLocked(local)
	a.opMu.Unlock()
	errs := []error{err}
	// The lists are fetched without opMu: offline, the retries would hold up
	// the user's own changes.
	for id, url := range jobs {
		builds, err := catalog.FetchBuilds(ctx, url)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s builds: %w", id, err))
			continue
		}
		b, ok := builds[local]
		if !ok {
			log.Printf("%s: no build for game %s yet", id, local)
			continue
		}
		errs = append(errs, a.installBuild(ctx, id, url, local, b))
	}
	return errors.Join(errs...)
}

// parkStaleLocked parks and unparks catalog mods with a builds.json for game
// build `local` and applies the change. It returns the builds.json URLs of
// the parked mods to fetch builds for, by mod id. opMu must be held.
func (a *App) parkStaleLocked(local string) (map[string]string, error) {
	jobs := map[string]string{}
	changed := false
	a.mu.Lock()
	for _, m := range a.state.Mods {
		if m.Source != mods.SourceCatalog {
			continue
		}
		// The catalog, once loaded, says whether the mod has builds.
		if a.catalog != nil && !a.state.FrogtechDisabled {
			if cm := a.catalog.Mod(m.ID); cm != nil && cm.Builds != m.Builds {
				m.Builds, changed = cm.Builds, true
			}
		}
		park := m.Builds != "" && stale(m.Game, local)
		if park != m.Parked {
			m.Parked, changed = park, true
			if park {
				log.Printf("%s is built for game %s, the game is on %s: parked until a build for it is out", m.ID, m.Game, local)
			} else {
				log.Printf("%s: unparked (game %s)", m.ID, local)
			}
		}
		// Builds are downloaded for enabled mods, and not while the Frogtech
		// repository is switched off (parking still keeps the game starting).
		if m.Parked && m.Enabled && !a.state.FrogtechDisabled {
			jobs[m.ID] = m.Builds
		}
	}
	if changed {
		a.saveLocked()
	}
	a.mu.Unlock()
	if !changed {
		return jobs, nil
	}
	return jobs, a.changedLocked()
}

// installBuild installs a parked mod's build for game `local` and applies.
func (a *App) installBuild(ctx context.Context, id, buildsURL, local string, b catalog.Build) error {
	defer a.beginOp("Installing " + id + " for game " + local)()
	a.mu.Lock()
	m := a.state.Mod(id)
	still := m != nil && m.Parked && m.Enabled && !a.state.FrogtechDisabled && m.Builds == buildsURL && a.clientVersionLocked() == local
	a.mu.Unlock()
	if !still {
		return nil // changed meanwhile; the next check sees it
	}
	if err := a.installBuildLocked(ctx, id, buildsURL, local, b); err != nil {
		return fmt.Errorf("%s build for game %s: %w", id, local, err)
	}
	return a.changedLocked()
}

// installBuildLocked installs a mod's build for game `local` (sha256 checked)
// in place of its files and unparks it. opMu must be held.
func (a *App) installBuildLocked(ctx context.Context, id, buildsURL, local string, b catalog.Build) error {
	a.mu.Lock()
	old := a.state.Mod(id)
	if old == nil {
		a.mu.Unlock()
		return fmt.Errorf("%s is not installed", id)
	}
	m := &mods.Mod{ID: id, Source: mods.SourceCatalog, Name: old.Name, Version: b.Version, Page: old.Page, Image: old.Image,
		Game: local, Builds: buildsURL}
	a.mu.Unlock()
	tmp, err := a.TempFile("build-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := fetch.Download(ctx, updateURL(b.URL), tmp, fetch.Checksum{Algo: "sha256", Hex: b.SHA256, Size: b.Size}); err != nil {
		return err
	}
	if err := a.installFiles(m, tmp, id+".vpk"); err != nil {
		return err
	}
	log.Printf("%s %s for game %s installed", m.Name, b.Version, local)
	return nil
}
