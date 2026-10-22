// Package queuealert sees when Deadlock finds a match, from the game's
// console.log (written only with the -condebug launch option). Deadlock has no
// accept step: a found match starts loading at once, so these lines mean
// "your game is loading".
package queuealert

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	// dedupe: a trigger this soon after an alert is the same match
	dedupeWindow = 60 * time.Second
	// logMissingAfter: a game running this long with an unchanged log isn't
	// writing one (no -condebug)
	logMissingAfter = 60 * time.Second
	// maxRead caps one poll's read (a log that grew a lot while unwatched)
	maxRead = 4 << 20
)

var (
	// "Lobby 174190353002534699 for Match 122156222 created" ("destroyed" when it ends)
	lobbyCreated = regexp.MustCompile(`Lobby \d+ for Match (\d+) created`)
	// "Received Steam datagram ticket for server steamid:… vport -1. match_id=122156222"
	datagramTicket = regexp.MustCompile(`Received Steam datagram ticket .*match_id=(\d+)`)
)

// Detect says whether a console.log line means a match was found, and its
// match id when the line has one.
func Detect(line string) (matchID string, ok bool) {
	if m := lobbyCreated.FindStringSubmatch(line); m != nil {
		return m[1], true
	}
	if m := datagramTicket.FindStringSubmatch(line); m != nil {
		return m[1], true
	}
	if strings.Contains(line, "CCitadel_PlayController::OnMatchFormed") {
		return "", true
	}
	return "", false
}

// Result is what one poll found.
type Result struct {
	Match      bool   // a match was found since the last poll
	MatchID    string // its id, when the log said it
	LogMissing bool   // the game runs but doesn't write console.log
}

// Watcher follows console.log while the game runs. Not safe for concurrent use.
type Watcher struct {
	path       string
	running    bool // the game ran at the last poll
	offset     int64
	partial    []byte // the last line read, still without its newline
	seenAt     time.Time
	modAtStart time.Time
	lastAlert  time.Time
	lastID     string
}

func NewWatcher(path string) *Watcher { return &Watcher{path: path} }

// Poll reads what the game wrote since the last poll. running says whether
// Deadlock runs now; now is the current time.
func (w *Watcher) Poll(running bool, now time.Time) Result {
	if !running {
		w.running = false
		return Result{}
	}
	st, err := os.Stat(w.path)
	if !w.running {
		// the game (or the loader) just started: what is in the log is old
		w.running, w.seenAt, w.partial = true, now, nil
		w.offset, w.modAtStart = 0, time.Time{}
		if err == nil {
			w.offset, w.modAtStart = st.Size(), st.ModTime()
		}
		return Result{}
	}
	var r Result
	if err != nil || st.ModTime().Equal(w.modAtStart) {
		r.LogMissing = now.Sub(w.seenAt) >= logMissingAfter
		return r
	}
	if st.Size() < w.offset {
		// a new session rewrote the log
		w.offset, w.partial = 0, nil
	}
	for _, line := range w.readLines(st.Size()) {
		id, ok := Detect(line)
		if !ok || w.duplicate(id, now) {
			continue
		}
		w.lastAlert, w.lastID = now, id
		r.Match, r.MatchID = true, id
	}
	return r
}

func (w *Watcher) duplicate(id string, now time.Time) bool {
	if w.lastAlert.IsZero() {
		return false
	}
	return (id != "" && id == w.lastID) || now.Sub(w.lastAlert) < dedupeWindow
}

// readLines returns the whole lines written since the last read.
func (w *Watcher) readLines(size int64) []string {
	if size == w.offset {
		return nil
	}
	f, err := os.Open(w.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	start := w.offset
	if size-start > maxRead {
		start, w.partial = size-maxRead, nil
	}
	buf := make([]byte, size-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return nil
	}
	w.offset = start + int64(n)
	data := append(w.partial, buf[:n]...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		w.partial = data
		return nil
	}
	w.partial = append([]byte(nil), data[end+1:]...)
	return strings.Split(strings.TrimRight(string(data[:end]), "\r"), "\n")
}
