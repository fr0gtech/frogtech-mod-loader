package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
)

// fakeAlert records what the PC side of a match alert did.
type fakeAlert struct {
	mu         sync.Mutex
	foreground bool
	flashes    int
	notes      [][2]string
}

func newFakeAlert(t *testing.T) *fakeAlert {
	f := &fakeAlert{}
	oldFlash, oldFG, oldNotify := flashGame, gameForeground, alertNotify
	flashGame = func() bool { f.mu.Lock(); defer f.mu.Unlock(); f.flashes++; return true }
	gameForeground = func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.foreground }
	alertNotify = func(title, msg string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.notes = append(f.notes, [2]string{title, msg})
	}
	t.Cleanup(func() { flashGame, gameForeground, alertNotify = oldFlash, oldFG, oldNotify })
	return f
}

func (f *fakeAlert) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flashes, len(f.notes)
}

func TestInitQueueAlertOnce(t *testing.T) {
	a := testApp(t)
	a.InitQueueAlert()
	if !a.state.Settings.QueueAlert || !a.state.QueueAlertInit {
		t.Fatalf("after first InitQueueAlert: QueueAlert=%v QueueAlertInit=%v, want both true", a.state.Settings.QueueAlert, a.state.QueueAlertInit)
	}
	s := a.state.Settings
	s.QueueAlert = false
	if err := a.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	a.InitQueueAlert()
	if a.state.Settings.QueueAlert {
		t.Fatal("InitQueueAlert turned the alert back on after the player turned it off")
	}
}

func TestOnMatchAlerts(t *testing.T) {
	f := newFakeAlert(t)
	a := testApp(t)
	a.onMatch("122156222")
	flashes, _ := f.counts()
	if flashes != 1 {
		t.Fatalf("flashes = %d, want 1", flashes)
	}
	if len(f.notes) != 1 || f.notes[0] != [2]string{"Match found", "Your Deadlock match is loading."} {
		t.Fatalf("notifications = %v", f.notes)
	}
}

func TestOnMatchForeground(t *testing.T) {
	f := newFakeAlert(t)
	f.foreground = true
	a := testApp(t)
	a.onMatch("1")
	if flashes, notes := f.counts(); flashes != 0 || notes != 0 {
		t.Fatalf("with Deadlock in front: %d flashes, %d notifications, want none", flashes, notes)
	}
}

// queueGame makes a game folder with an empty console.log and a running game.
func queueGame(t *testing.T, a *App) (log string, g *fakeGame) {
	t.Helper()
	game := t.TempDir()
	citadel, _, _ := steam.Paths(game)
	os.MkdirAll(citadel, 0o755)
	log = filepath.Join(citadel, "console.log")
	os.WriteFile(log, nil, 0o644)
	a.state.GameDir = game
	g = newFakeGame(t)
	g.set(true)
	return log, g
}

func appendMatch(t *testing.T, log string) {
	t.Helper()
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("10/22 08:55:31 Lobby 174190353002534699 for Match 122156222 created\n")
	f.Close()
	later := time.Now().Add(time.Minute)
	os.Chtimes(log, later, later)
}

func TestQueueTickAlerts(t *testing.T) {
	f := newFakeAlert(t)
	a := testApp(t)
	a.state.Settings.QueueAlert = true
	log, _ := queueGame(t, a)
	q := &queueWatch{}
	a.queueTick(q)
	appendMatch(t, log)
	a.queueTick(q)
	if flashes, notes := f.counts(); flashes != 1 || notes != 1 {
		t.Fatalf("after a match: %d flashes, %d notifications, want 1 and 1", flashes, notes)
	}
}

func TestQueueAlertOff(t *testing.T) {
	f := newFakeAlert(t)
	a := testApp(t)
	a.state.Settings.QueueAlert = false
	log, _ := queueGame(t, a)
	q := &queueWatch{}
	a.queueTick(q)
	appendMatch(t, log)
	a.queueTick(q)
	if flashes, notes := f.counts(); flashes != 0 || notes != 0 {
		t.Fatalf("alert off: %d flashes, %d notifications, want none", flashes, notes)
	}
}

func TestQueueLogMissingStatus(t *testing.T) {
	newFakeAlert(t)
	a := testApp(t)
	a.state.Settings.QueueAlert = true
	_, g := queueGame(t, a)
	q := &queueWatch{}
	a.queueTick(q)
	g.wait(61 * time.Second)
	a.queueTick(q)
	if !a.Status().QueueLogMissing {
		t.Fatal("QueueLogMissing false after a minute of an unchanged log")
	}
}
