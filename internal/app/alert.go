package app

// Match alerts: while Deadlock runs, its console.log is read once a second
// (internal/queuealert); a found match flashes the game in the taskbar, shows a
// notification and tells linked phones (through mods.frogtech.dev).

import (
	"log"
	"path/filepath"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/queuealert"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
)

// replaced in tests
var (
	flashGame      = platform.FlashGameWindow
	gameForeground = platform.GameIsForeground
	alertNotify    = winui.NotifyAlert
	queueInterval  = time.Second
)

// InitQueueAlert turns match alerts on once, for new and updated installs; the
// player's choice counts after that.
func (a *App) InitQueueAlert() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.QueueAlertInit {
		return
	}
	a.state.QueueAlertInit = true
	a.state.Settings.QueueAlert = true
	a.saveLocked()
}

// queueWatch is the watcher for the current game folder's console.log.
type queueWatch struct {
	path string
	w    *queuealert.Watcher
}

// watchQueue runs queueTick every queueInterval until quit.
func (a *App) watchQueue() {
	t := time.NewTicker(queueInterval)
	defer t.Stop()
	q := &queueWatch{}
	for {
		a.queueTick(q)
		select {
		case <-a.quit:
			return
		case <-t.C:
		}
	}
}

// queueTick reads what the game wrote since the last tick and alerts on a match.
func (a *App) queueTick(q *queueWatch) {
	a.mu.Lock()
	on, dir := a.state.Settings.QueueAlert, a.state.GameDir
	a.mu.Unlock()
	if !on || dir == "" {
		a.setQueueLogMissing(false)
		q.w = nil
		return
	}
	citadel, _, _ := steam.Paths(dir)
	if path := filepath.Join(citadel, "console.log"); q.w == nil || q.path != path {
		q.path, q.w = path, queuealert.NewWatcher(path)
	}
	r := q.w.Poll(gameRunning(), now())
	a.setQueueLogMissing(r.LogMissing)
	if r.Match {
		a.onMatch(r.MatchID)
	}
}

func (a *App) setQueueLogMissing(v bool) {
	a.mu.Lock()
	a.queueLogMissing = v
	a.mu.Unlock()
}

// onMatch tells the player their match was found, unless they're looking at the game.
func (a *App) onMatch(id string) {
	if gameForeground() {
		log.Printf("match %s found (Deadlock in front: no alert)", id)
		return
	}
	log.Printf("match %s found: alerting", id)
	flashGame()
	alertNotify("Match found", "Your Deadlock match is loading.")
	go a.sendPhone("match")
}

// sendPhone tells linked phones (filled in with the phone link).
func (a *App) sendPhone(kind string) {}
