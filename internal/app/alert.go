package app

// Match alerts: while Deadlock runs, its console.log is read once a second
// (internal/queuealert); a found match flashes the game in the taskbar, shows a
// notification and tells linked phones (through mods.frogtech.dev).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"

	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/queuealert"
	"github.com/fr0gtech/frogtech-mod-loader/internal/secrets"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
)

// replaced in tests
var (
	flashGame      = platform.FlashGameWindow
	gameForeground = platform.GameIsForeground
	alertNotify    = winui.NotifyAlert
	queueInterval  = time.Second
	// alertAPI sends match alerts to linked phones (frogtech-site: /api/alert)
	alertAPI        = "https://mods.frogtech.dev/api/alert"
	phoneRetryDelay = 2 * time.Second
)

// alertPage is where a phone turns match alerts on; the key follows the #.
const alertPage = "https://mods.frogtech.dev/alert#"

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

// onMatch tells the player their match was found: linked phones always, the PC
// unless they're looking at the game.
func (a *App) onMatch(id string) {
	// phones always: a player who queued and walked away often left the game in front
	go a.sendPhone("match")
	if gameForeground() {
		log.Printf("match %s found (Deadlock in front: no PC alert)", id)
		return
	}
	log.Printf("match %s found: alerting", id)
	flashGame()
	alertNotify("Match found", "Your Deadlock match is loading.")
}

// alertKey returns the phone link key, making one when create is set ("" without).
func (a *App) alertKey(create bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.AlertKey != "" {
		key, err := secrets.Open(a.state.AlertKey)
		if err == nil {
			return key, nil
		}
		// sealed for another Windows user or PC (state.json copied over): start over
		log.Printf("phone link key unreadable (%v): making a new one", err)
		a.state.AlertKey = ""
	}
	if !create {
		return "", nil
	}
	b := make([]byte, 16)
	rand.Read(b)
	key := base64.RawURLEncoding.EncodeToString(b)
	sealed, err := secrets.Seal(key)
	if err != nil {
		return "", err
	}
	a.state.AlertKey = sealed
	return key, a.saveLocked()
}

// AlertLink is the address a phone opens to get match alerts.
func (a *App) AlertLink() (string, error) {
	key, err := a.alertKey(true)
	if err != nil {
		return "", err
	}
	return alertPage + key, nil
}

// AlertPhones counts the phones linked to this loader (0 before any link).
func (a *App) AlertPhones(ctx context.Context) (int, error) {
	key, err := a.alertKey(false)
	if err != nil || key == "" {
		return 0, err
	}
	var r struct {
		Phones int `json:"phones"`
	}
	err = postAlert(ctx, map[string]any{"key": key, "status": true}, &r)
	return r.Phones, err
}

// AlertTest sends a test alert to the linked phones.
func (a *App) AlertTest(ctx context.Context) error {
	key, err := a.alertKey(false)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("Link your phone first")
	}
	err = postAlert(ctx, map[string]any{"key": key, "send": "test"}, nil)
	var se *fetch.StatusError
	if errors.As(err, &se) && se.Code == http.StatusTooManyRequests {
		return errors.New("Wait a few seconds before sending another test")
	}
	return err
}

// AlertUnlink drops every linked phone; the next link gets a new key.
func (a *App) AlertUnlink(ctx context.Context) error {
	key, err := a.alertKey(false)
	if err != nil || key == "" {
		return err
	}
	if err := postAlert(ctx, map[string]any{"key": key, "unlink": true}, nil); err != nil {
		return err
	}
	a.mu.Lock()
	a.state.AlertKey = ""
	err = a.saveLocked()
	a.mu.Unlock()
	return err
}

// sendPhone tells the linked phones (kind "match" or "test"); errors are only
// logged, the PC alert has already happened.
func (a *App) sendPhone(kind string) {
	key, err := a.alertKey(false)
	if err != nil || key == "" {
		return
	}
	body := map[string]any{"key": key, "send": kind}
	for try := 0; try < 2; try++ {
		if try > 0 {
			time.Sleep(phoneRetryDelay)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = postAlert(ctx, body, nil)
		cancel()
		var se *fetch.StatusError
		if err == nil || (errors.As(err, &se) && se.Code < 500) {
			break // done, or an answer a retry won't change
		}
	}
	if err != nil {
		log.Printf("phone alert: %v", err)
	}
}

// postAlert POSTs body to alertAPI and decodes the answer into out (if not nil).
func postAlert(ctx context.Context, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, alertAPI, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := fetch.Client.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach mods.frogtech.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &fetch.StatusError{URL: alertAPI, Code: resp.StatusCode}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
