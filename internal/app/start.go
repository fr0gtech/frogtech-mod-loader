package app

import (
	"errors"
	"log"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/crashdump"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gameinfo"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
)

// Seams for tests: the game process, links handed to Windows (steam://) and
// the clock.
var (
	gameRunning = platform.IsGameRunning
	openURL     = platform.OpenURL
	now         = time.Now
)

const (
	// A crash dump this soon after the loader started the game means it didn't start.
	startWindow = 60 * time.Second
	// Running this long after a dump means the game started fine since.
	healthyAfter = 60 * time.Second
	// "Start without mods" gives up waiting for the game after this.
	noModsTimeout = 3 * time.Minute
	// Dumps older than this aren't shown when the loader starts.
	dumpMaxAge = 12 * time.Hour
	// On the first run, look back this far for a failed start.
	firstLookBack = 30 * time.Minute
)

// ErrRepairing: Steam is still checking the game files for "Repair".
var ErrRepairing = errors.New("Steam is still checking Deadlock's files. Mods come back when it's done")

// StartFail is what the page shows when Deadlock didn't start.
type StartFail struct {
	Message     string    `json:"message,omitempty"` // the game's own error line
	Dump        string    `json:"dump,omitempty"`
	Time        time.Time `json:"time"`
	Corrupt     bool      `json:"corrupt"`     // Steam has the game marked as damaged
	WithoutMods bool      `json:"withoutMods"` // it happened during "Start without mods"
}

type repairView struct {
	Started  time.Time `json:"started"`
	Checking bool      `json:"checking"` // Steam was seen working on the files
}

// modsOffLocked: the game should run without the loader's lines in gameinfo.gi.
func (a *App) modsOffLocked() bool {
	return a.state.ModsOff || a.state.Repair != nil || a.state.NoModsRun != nil
}

// SetModsOn is the main switch. Off takes every mod file the loader wrote out
// of the game and gives gameinfo.gi back to Valve's version; on puts both
// back. While the game runs, the files follow once it is closed.
func (a *App) SetModsOn(on bool) error {
	defer a.beginOp(map[bool]string{true: "Turning mods on", false: "Turning mods off"}[on])()
	a.mu.Lock()
	if a.state.Repair != nil {
		a.mu.Unlock()
		return ErrRepairing
	}
	a.state.ModsOff = !on
	a.state.PendingApply = true
	a.saveLocked()
	a.mu.Unlock()
	log.Printf("mods switched %s", map[bool]string{true: "on", false: "off"}[on])
	dir := a.GameDir()
	if dir == "" {
		return nil
	}
	if _, err := a.EnsureGameinfo(); err != nil {
		log.Printf("gameinfo: %v", err)
	}
	if !on && !gameRunning() {
		_, addons, _ := steam.Paths(dir)
		a.mu.Lock()
		n, err := a.store.RemoveOurPaks(a.state, addons)
		a.saveLocked()
		a.mu.Unlock()
		if err != nil {
			return err
		}
		log.Printf("took %d mod file(s) out of the game", n)
	}
	return a.changedLocked()
}

// ModsOn reports the main switch (for the tray).
func (a *App) ModsOn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.state.ModsOff
}

// SetModsOnFromTray flips the main switch from the tray menu.
func (a *App) SetModsOnFromTray(on bool) {
	if err := a.SetModsOn(on); err != nil {
		log.Printf("mods switch: %v", err)
		a.setErr(err)
	}
}

// Repair is the fix for a game that won't start: take the mod files out,
// give gameinfo.gi back to Valve's version and ask Steam to check the game
// files. The watcher turns mods back on once Steam is done.
func (a *App) Repair() error {
	defer a.beginOp("Repairing Deadlock")()
	dir := a.GameDir()
	if dir == "" {
		return ErrNoGame
	}
	if gameRunning() {
		return ErrGameRunning
	}
	_, addons, _ := steam.Paths(dir)
	st, _ := steam.ReadAppState(dir)
	a.mu.Lock()
	n, err := a.store.RemoveOurPaks(a.state, addons)
	if err != nil {
		log.Printf("repair: %v", err)
	}
	a.state.Repair = &mods.Repair{Started: now(), LastUpdated: st.LastUpdated}
	a.state.NoModsRun = nil
	a.startFail = nil
	a.saveLocked()
	a.mu.Unlock()
	if _, err := gameinfo.Unfix(dir); err != nil {
		log.Printf("repair: gameinfo: %v", err)
	}
	log.Printf("repair: took %d mod file(s) out, asking Steam to check the game files", n)
	if err := openURL("steam://validate/" + steam.AppID); err != nil {
		a.finishRepairLocked()
		return err
	}
	return nil
}

// FinishRepair stops waiting for Steam and turns mods back on ("Skip waiting").
func (a *App) FinishRepair() error {
	defer a.beginOp("Turning mods back on")()
	return a.finishRepairLocked()
}

// finishRepairLocked turns mods back on after a repair (opMu must be held).
func (a *App) finishRepairLocked() error {
	a.mu.Lock()
	if a.state.Repair == nil {
		a.mu.Unlock()
		return nil
	}
	a.state.Repair = nil
	a.state.StartSeen = now()
	a.state.PendingApply = true
	a.startFail = nil
	a.saveLocked()
	a.mu.Unlock()
	log.Printf("repair: done, putting mods back")
	return a.changedLocked()
}

// PlayWithoutMods starts the game once with Valve's gameinfo.gi (no mods are
// loaded); the loader's lines go back in when the game is closed.
func (a *App) PlayWithoutMods() error {
	defer a.beginOp("Starting Deadlock without mods")()
	dir := a.GameDir()
	if dir == "" {
		return ErrNoGame
	}
	if gameRunning() {
		return ErrGameRunning
	}
	t := now()
	a.mu.Lock()
	if a.state.Repair != nil {
		a.mu.Unlock()
		return ErrRepairing
	}
	a.state.NoModsRun = &mods.NoModsRun{Started: t}
	a.state.StartSeen = t
	a.startFail = nil
	a.launchedAt, a.noModsFrom, a.noModsTo = t, t, time.Time{}
	a.saveLocked()
	a.mu.Unlock()
	if _, err := gameinfo.Unfix(dir); err != nil {
		a.endNoMods()
		return err
	}
	log.Printf("launching Deadlock without mods")
	if err := openURL("steam://rungameid/" + steam.AppID); err != nil {
		a.endNoMods()
		return err
	}
	return nil
}

// endNoMods puts gameinfo.gi back after "Start without mods".
func (a *App) endNoMods() {
	a.mu.Lock()
	if a.state.NoModsRun == nil {
		a.mu.Unlock()
		return
	}
	a.state.NoModsRun = nil
	a.noModsTo = now()
	a.saveLocked()
	a.mu.Unlock()
	log.Printf("game closed: mods back on")
	if _, err := a.EnsureGameinfo(); err != nil {
		log.Printf("gameinfo: %v", err)
	}
}

// DismissStartFail hides the "Deadlock didn't start" banner until the next
// failed start.
func (a *App) DismissStartFail() error {
	dir := a.GameDir()
	st, _ := steam.ReadAppState(dir)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.StartSeen = now()
	if st.FilesCorrupt() {
		a.state.CorruptSeen = st.LastUpdated
	}
	a.startFail = nil
	return a.saveLocked()
}

// watchStart runs with the gameinfo watcher: it moves "Repair" and "Start
// without mods" along and notices failed starts (also of launches outside
// the loader).
func (a *App) watchStart() {
	dir := a.GameDir()
	if dir == "" {
		return
	}
	running := gameRunning()
	t := now()
	app, appErr := steam.ReadAppState(dir)

	a.mu.Lock()
	if !running {
		a.runningSince = time.Time{}
	} else if a.runningSince.IsZero() {
		a.runningSince = t
	}
	runningSince, launched := a.runningSince, a.launchedAt
	seen, corruptSeen := a.state.StartSeen, a.state.CorruptSeen
	if seen.Before(t.Add(-dumpMaxAge)) {
		seen = t.Add(-dumpMaxAge)
	}
	nm, rep := a.state.NoModsRun, a.state.Repair
	if nm != nil && running && !nm.SawRunning {
		nm.SawRunning = true
		a.saveLocked()
	}
	if rep != nil && appErr == nil && !app.FullyInstalled() && !rep.SawCheck {
		rep.SawCheck = true
		log.Printf("repair: Steam is checking the game files")
		a.saveLocked()
	}
	noModsFrom, noModsTo := a.noModsFrom, a.noModsTo
	a.mu.Unlock()

	if nm != nil && !running && (nm.SawRunning || t.Sub(nm.Started) > noModsTimeout) {
		a.endNoMods()
	}
	if rep != nil {
		// Steam is done when the manifest says fully installed again after
		// working on the game (or after it already finished: LastUpdated moved).
		if appErr == nil && app.FullyInstalled() && (rep.SawCheck || app.LastUpdated != rep.LastUpdated) && a.opMu.TryLock() {
			if err := a.finishRepairLocked(); err != nil {
				a.setErr(err)
			}
			a.opMu.Unlock()
		}
		return
	}

	var fail *StartFail
	for _, d := range crashdump.Find(dir, seen) {
		soon := !launched.IsZero() && !d.Time.Before(launched) && d.Time.Sub(launched) <= startWindow
		if d.Error || soon {
			fail = &StartFail{Dump: d.Name, Time: d.Time, Message: a.dumpMessage(d.Path)}
			break
		}
	}
	if fail != nil && running && runningSince.After(fail.Time) && t.Sub(runningSince) > healthyAfter {
		// The game has been running a while since: it started fine.
		a.mu.Lock()
		a.state.StartSeen = fail.Time
		a.saveLocked()
		a.mu.Unlock()
		fail = nil
	}
	corrupt := appErr == nil && !running && app.FilesCorrupt() && app.LastUpdated != corruptSeen
	if fail == nil && corrupt {
		fail = &StartFail{Time: t}
	}
	if fail != nil {
		fail.Corrupt = corrupt
		fail.WithoutMods = !noModsFrom.IsZero() && !fail.Time.Before(noModsFrom) &&
			(noModsTo.IsZero() || !fail.Time.After(noModsTo.Add(5*time.Second)))
	}
	a.mu.Lock()
	a.startFail = fail
	a.mu.Unlock()
}

// dumpMessage reads (once) the game's error line from a dump.
func (a *App) dumpMessage(path string) string {
	a.mu.Lock()
	m, ok := a.dumpMsgs[path]
	a.mu.Unlock()
	if ok {
		return m
	}
	m = crashdump.Message(path)
	a.mu.Lock()
	a.dumpMsgs[path] = m
	a.mu.Unlock()
	return m
}
