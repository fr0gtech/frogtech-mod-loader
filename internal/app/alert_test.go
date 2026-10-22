package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// fakeSite stands in for mods.frogtech.dev/api/alert: it records the bodies
// and answers with the given status codes in turn (then 200).
type fakeSite struct {
	mu     sync.Mutex
	bodies []map[string]any
	codes  []int
	phones int
}

func newFakeSite(t *testing.T, codes ...int) *fakeSite {
	f := &fakeSite{codes: codes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		code := http.StatusOK
		if len(f.codes) > 0 {
			code, f.codes = f.codes[0], f.codes[1:]
		}
		phones := f.phones
		f.mu.Unlock()
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"ok": code == 200, "phones": phones})
	}))
	t.Cleanup(srv.Close)
	old, oldRetry := alertAPI, phoneRetryDelay
	alertAPI, phoneRetryDelay = srv.URL, time.Millisecond
	t.Cleanup(func() { alertAPI, phoneRetryDelay = old, oldRetry })
	return f
}

func (f *fakeSite) got() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

var linkRE = regexp.MustCompile(`^https://mods\.frogtech\.dev/alert#([A-Za-z0-9_-]{22})$`)

func TestAlertLinkStable(t *testing.T) {
	a := testApp(t)
	l1, err := a.AlertLink()
	if err != nil {
		t.Fatal(err)
	}
	l2, _ := a.AlertLink()
	if l1 != l2 {
		t.Fatalf("links differ: %q %q", l1, l2)
	}
	m := linkRE.FindStringSubmatch(l1)
	if m == nil {
		t.Fatalf("link %q", l1)
	}
	data, _ := os.ReadFile(filepath.Join(a.DataDir(), "state.json"))
	if strings.Contains(string(data), m[1]) {
		t.Fatal("state.json holds the key in plain text")
	}
}

func TestSendPhoneBody(t *testing.T) {
	site := newFakeSite(t)
	a := testApp(t)
	link, _ := a.AlertLink()
	a.sendPhone("match")
	b := site.got()
	if len(b) != 1 || b[0]["send"] != "match" || b[0]["key"] != linkRE.FindStringSubmatch(link)[1] {
		t.Fatalf("site got %v", b)
	}
}

func TestSendPhoneNoKey(t *testing.T) {
	site := newFakeSite(t)
	a := testApp(t)
	a.sendPhone("match")
	if b := site.got(); len(b) != 0 {
		t.Fatalf("site got %v without a linked phone", b)
	}
}

func TestSendPhoneRetry(t *testing.T) {
	site := newFakeSite(t, http.StatusServiceUnavailable)
	a := testApp(t)
	a.AlertLink()
	a.sendPhone("match")
	if n := len(site.got()); n != 2 {
		t.Fatalf("requests = %d, want 2 (one retry after a 503)", n)
	}
}

func TestSendPhoneSiteDown(t *testing.T) {
	a := testApp(t)
	old, oldRetry := alertAPI, phoneRetryDelay
	alertAPI, phoneRetryDelay = "http://127.0.0.1:1/api/alert", time.Millisecond
	t.Cleanup(func() { alertAPI, phoneRetryDelay = old, oldRetry })
	a.AlertLink()
	a.sendPhone("match") // returns, only logs
}

func TestAlertUnlinkNewKey(t *testing.T) {
	site := newFakeSite(t)
	a := testApp(t)
	l1, _ := a.AlertLink()
	if err := a.AlertUnlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	l2, _ := a.AlertLink()
	if l1 == l2 {
		t.Fatal("same link after unlinking")
	}
	b := site.got()
	if len(b) != 1 || b[0]["unlink"] != true || b[0]["key"] != linkRE.FindStringSubmatch(l1)[1] {
		t.Fatalf("site got %v", b)
	}
}

func TestAlertPhones(t *testing.T) {
	site := newFakeSite(t)
	site.phones = 2
	a := testApp(t)
	if n, err := a.AlertPhones(context.Background()); err != nil || n != 0 || len(site.got()) != 0 {
		t.Fatalf("no key: %d %v, %d requests; want 0, nil, none", n, err, len(site.got()))
	}
	a.AlertLink()
	if n, err := a.AlertPhones(context.Background()); err != nil || n != 2 {
		t.Fatalf("phones = %d %v, want 2", n, err)
	}
	if b := site.got(); b[0]["status"] != true {
		t.Fatalf("site got %v", b)
	}
}

func TestAlertTest429(t *testing.T) {
	newFakeSite(t, http.StatusTooManyRequests)
	a := testApp(t)
	a.AlertLink()
	err := a.AlertTest(context.Background())
	if err == nil || err.Error() != "Wait a few seconds before sending another test" {
		t.Fatalf("err = %v", err)
	}
}

func TestOnMatchForegroundStillSendsPhone(t *testing.T) {
	f := newFakeAlert(t)
	f.foreground = true
	site := newFakeSite(t)
	a := testApp(t)
	a.AlertLink()
	a.onMatch("1")
	deadline := time.Now().Add(2 * time.Second)
	for len(site.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b := site.got(); len(b) != 1 || b[0]["send"] != "match" {
		t.Fatalf("with Deadlock in front the phone got %v, want one send:match", b)
	}
}

func TestAlertKeyUnreadableMakesNewOne(t *testing.T) {
	a := testApp(t)
	a.state.AlertKey = "not a sealed key!"
	link, err := a.AlertLink()
	if err != nil || !linkRE.MatchString(link) {
		t.Fatalf("AlertLink with an unreadable key = %q, %v; want a new link", link, err)
	}
}
