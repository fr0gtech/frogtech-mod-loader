package queuealert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// logFile is a console.log in a temp dir with a fixed modification time.
type logFile struct {
	t    *testing.T
	path string
	mod  time.Time
}

func newLog(t *testing.T, content string) *logFile {
	l := &logFile{t: t, path: filepath.Join(t.TempDir(), "console.log"), mod: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)}
	l.write(content)
	return l
}

func (l *logFile) write(content string) {
	l.t.Helper()
	if err := os.WriteFile(l.path, []byte(content), 0o644); err != nil {
		l.t.Fatal(err)
	}
	l.touch()
}

func (l *logFile) append(s string) {
	l.t.Helper()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		l.t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
	l.touch()
}

// touch moves the modification time on (the game wrote to the log).
func (l *logFile) touch() {
	l.mod = l.mod.Add(time.Second)
	os.Chtimes(l.path, l.mod, l.mod)
}

var t0 = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

func TestDetectFixtures(t *testing.T) {
	hits := 0
	for _, line := range strings.Split(fixture(t, "match_found.log"), "\n") {
		if id, ok := Detect(line); ok {
			hits++
			if id != "" && id != "122156222" {
				t.Errorf("Detect(%q) id = %q", line, id)
			}
		}
	}
	if hits == 0 {
		t.Fatal("no line of match_found.log detected")
	}
	for _, line := range strings.Split(fixture(t, "queue_start.log"), "\n") {
		if _, ok := Detect(line); ok {
			t.Errorf("Detect(%q) = true, want false", line)
		}
	}
}

func TestPollStartsAtEnd(t *testing.T) {
	l := newLog(t, fixture(t, "match_found.log"))
	w := NewWatcher(l.path)
	if r := w.Poll(true, t0); r != (Result{}) {
		t.Fatalf("first poll = %+v, want nothing", r)
	}
	if r := w.Poll(true, t0.Add(time.Second)); r != (Result{}) {
		t.Fatalf("second poll = %+v, want nothing", r)
	}
}

func TestPollFiresOncePerMatch(t *testing.T) {
	l := newLog(t, "")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	l.append(fixture(t, "match_found.log"))
	r := w.Poll(true, t0.Add(time.Second))
	if !r.Match || r.MatchID != "122156222" {
		t.Fatalf("poll after match = %+v, want Match with id 122156222", r)
	}
	if r := w.Poll(true, t0.Add(2*time.Second)); r.Match {
		t.Fatalf("poll again = %+v, want no Match", r)
	}
}

func TestPollDedupeWindow(t *testing.T) {
	l := newLog(t, "")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	l.append(fixture(t, "match_found.log"))
	if r := w.Poll(true, t0); !r.Match {
		t.Fatal("first match not detected")
	}
	l.append("10/22 09:00:30 [Citadel Play Controller] CCitadel_PlayController::OnMatchFormed\n")
	if r := w.Poll(true, t0.Add(30*time.Second)); r.Match {
		t.Fatalf("trigger 30 s later = %+v, want no Match", r)
	}
	l.append("10/22 09:01:01 Lobby 1 for Match 999 created\n")
	if r := w.Poll(true, t0.Add(61*time.Second)); !r.Match || r.MatchID != "999" {
		t.Fatalf("new match 61 s later = %+v, want Match 999", r)
	}
}

func TestPollRestartedLog(t *testing.T) {
	l := newLog(t, strings.Repeat("x", 5000)+"\n")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	l.write(strings.Repeat("y", 200) + "\n" + "10/22 10:00:00 Lobby 2 for Match 555 created\n")
	if r := w.Poll(true, t0.Add(time.Second)); !r.Match || r.MatchID != "555" {
		t.Fatalf("poll after restart = %+v, want Match 555", r)
	}
}

func TestPollPartialLine(t *testing.T) {
	l := newLog(t, "")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	l.append("10/22 09:00:01 Lobby 1 for Match 7 cre")
	if r := w.Poll(true, t0.Add(time.Second)); r.Match {
		t.Fatalf("half line = %+v, want no Match", r)
	}
	l.append("ated\n")
	if r := w.Poll(true, t0.Add(2*time.Second)); !r.Match || r.MatchID != "7" {
		t.Fatalf("completed line = %+v, want Match 7", r)
	}
}

func TestPollLogMissing(t *testing.T) {
	l := newLog(t, "old session\n")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	if r := w.Poll(true, t0.Add(59*time.Second)); r.LogMissing {
		t.Fatal("LogMissing after 59 s, want only from 60 s")
	}
	if r := w.Poll(true, t0.Add(60*time.Second)); !r.LogMissing {
		t.Fatal("LogMissing false after 60 s of an unchanged log")
	}
	l.append("new line\n")
	if r := w.Poll(true, t0.Add(61*time.Second)); r.LogMissing {
		t.Fatal("LogMissing still true after the log changed")
	}
}

func TestPollLogMissingNoFile(t *testing.T) {
	w := NewWatcher(filepath.Join(t.TempDir(), "console.log"))
	w.Poll(true, t0)
	if r := w.Poll(true, t0.Add(60*time.Second)); !r.LogMissing {
		t.Fatal("LogMissing false with no console.log at all")
	}
}

func TestPollNotRunningResets(t *testing.T) {
	l := newLog(t, "")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	if r := w.Poll(false, t0.Add(time.Second)); r != (Result{}) {
		t.Fatalf("not running = %+v, want nothing", r)
	}
	l.append(fixture(t, "match_found.log"))
	// the game starts again: what is in the log already is old
	if r := w.Poll(true, t0.Add(2*time.Second)); r.Match {
		t.Fatalf("first poll of a new run = %+v, want no Match", r)
	}
}

// Windows may not move the modification time while the game keeps the log
// open: a grown file is read anyway, and it counts as written.
func TestPollGrowsWithoutModTime(t *testing.T) {
	l := newLog(t, "")
	w := NewWatcher(l.path)
	w.Poll(true, t0)
	mod := l.mod
	l.append("10/22 09:00:01 Lobby 1 for Match 42 created\n")
	os.Chtimes(l.path, mod, mod)
	if r := w.Poll(true, t0.Add(61*time.Second)); !r.Match || r.MatchID != "42" || r.LogMissing {
		t.Fatalf("grown log with an unchanged time = %+v, want Match 42 and no LogMissing", r)
	}
}
