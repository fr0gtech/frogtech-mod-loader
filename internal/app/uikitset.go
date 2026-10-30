package app

// ---- UI Kit theme sets ----
//
// A player's themes, built by mods.frogtech.dev into one VPK that takes the
// place of the starter set (uikit_set_dir.vpk) in UI Kit's store folder. A
// set is built for one game build and one UI Kit core, and one built for
// another crashes the game at start like a stale UI Kit does. So a set is
// only ever installed as the site built it for the local game and the
// installed core: from a link the site is asked for that build first, and
// after a game update (or a UI Kit release) the build or release puts the
// starter set back (installFiles replaces the whole folder) and the site is
// asked again; until it has built the set, the starter set runs.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
	"github.com/fr0gtech/frogtech-mod-loader/internal/links"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
)

const (
	uikitID      = "uikit"
	uikitSetFile = "uikit_set_dir.vpk"
)

// errSetLater: the site couldn't build the set for this game and core yet
// (it doesn't serve the game, it has another core, or it can't be reached).
// The starter set runs and the loader asks again on the parked cadence.
type errSetLater struct{ why string }

func (e *errSetLater) Error() string { return e.why }

// errSetBroken: the site built the set for this game and its themes don't
// build (422), are too big for it (413) or the site refuses the request
// (400). Not asked again for this game and
// core: the player fixes them on the site and opens a new link.
type errSetBroken struct{ why string }

func (e *errSetBroken) Error() string { return e.why }

// siteBase is the Frogtech site's origin, taken from the catalog URL (so the
// tests' fake catalog serves the set API too).
func siteBase() string {
	u, err := url.Parse(catalog.FrogtechURL)
	if err != nil || u.Host == "" {
		return "https://mods.frogtech.dev"
	}
	return u.Scheme + "://" + u.Host
}

// markSetBroken records that the site can't build the player's set for game
// and core: the page says so and the site is not asked again for those.
func markSetBroken(m *mods.Mod, game, core string) {
	m.SetNote = "Your themes don't build for game " + game + ": fix them on mods.frogtech.dev/themes"
	m.SetFailed = game + "/" + core
}

// wantedSet is the set the player wants in the game: a newly chosen one
// waiting to be built, else the one last installed.
func wantedSet(m *mods.Mod) string {
	if m.SetNext != "" {
		return m.SetNext
	}
	return m.Set
}

// requestRebuild asks the site for set `key` built for game and core and
// returns the built set's key (the same key when nothing changed).
func requestRebuild(ctx context.Context, key, game, core string) (string, error) {
	b, _ := json.Marshal(map[string]string{"game": game, "core": core})
	u := siteBase() + "/api/uikit/sets/" + key + "/rebuild"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := fetch.Client.Do(req)
	if err != nil {
		return "", &errSetLater{"couldn't reach the site: " + err.Error()}
	}
	defer resp.Body.Close()
	var out struct {
		Key      string `json:"key"`
		Error    string `json:"error"`
		Core     string `json:"core"`
		Problems []struct {
			File    string `json:"file"`
			Line    any    `json:"line"`
			Message string `json:"message"`
		} `json:"problems"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	switch resp.StatusCode {
	case http.StatusOK:
		if !links.IsSetKey(out.Key) {
			return "", &errSetLater{"the site answered an unreadable key"}
		}
		return out.Key, nil
	case http.StatusNotFound:
		return "", &errSetLater{"the site can't build themes for game " + game + " yet"}
	case http.StatusConflict:
		return "", &errSetLater{"the site builds themes for UI Kit " + out.Core + ", this is UI Kit " + core}
	case http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge, http.StatusBadRequest:
		var ps []string
		for _, p := range out.Problems {
			s := p.Message
			if p.File != "" {
				s = fmt.Sprintf("%s:%v: %s", p.File, p.Line, p.Message)
			}
			ps = append(ps, s)
		}
		if out.Error != "" {
			ps = append(ps, out.Error)
		}
		return "", &errSetBroken{"your themes don't build for game " + game + ": " + strings.Join(ps, "; ")}
	}
	msg := fmt.Sprintf("the site answered HTTP %d", resp.StatusCode)
	if out.Error != "" {
		msg += ": " + out.Error
	}
	return "", &errSetLater{msg}
}

// InstallSet installs the player's theme set `key` (a frogmod://uikit-set
// link) in place of UI Kit's starter set, installing UI Kit first when it
// isn't. The site builds the set for the local game and the installed core
// first; when it can't yet, the set is recorded and installed by a later
// check (errSetLater), the starter set running meanwhile.
func (a *App) InstallSet(ctx context.Context, key string) error {
	if !links.IsSetKey(key) {
		return fmt.Errorf("%q is not a theme set", key)
	}
	a.mu.Lock()
	off := a.state.FrogtechDisabled
	a.mu.Unlock()
	if off {
		return errors.New("the Frogtech repository is switched off (Settings > Repositories): turn it on to install your themes")
	}
	a.mu.Lock()
	installed := a.state.Mod(uikitID) != nil
	haveCatalog := a.catalog != nil && a.catalog.Mod(uikitID) != nil
	a.mu.Unlock()
	if !installed {
		if !haveCatalog {
			if err := a.RefreshCatalog(ctx); err != nil {
				return fmt.Errorf("couldn't load the Frogtech mod list: %w", err)
			}
		}
		if err := a.InstallCatalogMod(ctx, uikitID); err != nil {
			return fmt.Errorf("UI Kit: %w", err)
		}
	}
	local := a.clientVersion()
	if local == "" {
		return errors.New("the game's version is unknown: set the Deadlock folder first")
	}
	a.mu.Lock()
	m := a.state.Mod(uikitID)
	if m == nil {
		a.mu.Unlock()
		return errors.New("UI Kit is not installed")
	}
	parked := m.Parked || stale(m.Game, local)
	core := m.Version
	a.mu.Unlock()

	newKey := ""
	var err error
	if parked {
		err = &errSetLater{"UI Kit waits for a version for game " + local}
	} else {
		newKey, err = requestRebuild(ctx, key, local, core)
	}
	defer a.beginOp("Installing your themes")()
	if err != nil {
		// Recorded for a later check; the set file in the store (the
		// starter set or the player's previous set) stays meanwhile.
		var broken *errSetBroken
		a.mu.Lock()
		if m := a.state.Mod(uikitID); m != nil {
			m.Enabled = true
			m.SetNext, m.SetNote, m.SetFailed = key, "", ""
			if errors.As(err, &broken) {
				markSetBroken(m, local, core)
			}
		}
		a.saveLocked()
		a.mu.Unlock()
		if cerr := a.changedLocked(); cerr != nil {
			return cerr
		}
		if broken != nil {
			return err
		}
		log.Printf("UI Kit set %s: %v; the starter set runs until it is built", key, err)
		a.TriggerCheck()
		return &errSetLater{"Your themes go in once they're built for game " + local + " (" + err.Error() + ")."}
	}
	a.mu.Lock()
	m = a.state.Mod(uikitID)
	still := m != nil && m.Version == core && !m.Parked && !stale(m.Game, local) && a.clientVersionLocked() == local
	a.mu.Unlock()
	if !still {
		return errors.New("UI Kit changed meanwhile: open the link again")
	}
	if err := a.installSetLocked(ctx, newKey, local, core); err != nil {
		return err
	}
	a.mu.Lock()
	if m := a.state.Mod(uikitID); m != nil {
		m.Enabled = true
	}
	a.mu.Unlock()
	return a.changedLocked()
}

// installSetLocked downloads set `key`, built for game and core, in place of
// UI Kit's set file and records it. opMu must be held.
func (a *App) installSetLocked(ctx context.Context, key, game, core string) error {
	a.mu.Lock()
	m := a.state.Mod(uikitID)
	ok := m != nil && slices.Contains(m.Files, uikitSetFile)
	a.mu.Unlock()
	if !ok {
		return errors.New("this UI Kit has no " + uikitSetFile + " to replace")
	}
	tmp, err := a.TempFile("set-*.vpk")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := fetch.Download(ctx, siteBase()+"/api/uikit/sets/"+key+".vpk", tmp, fetch.Checksum{Algo: "none"}); err != nil {
		return fmt.Errorf("downloading your themes: %w", err)
	}
	head := make([]byte, 4)
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil || !mods.IsVPKHeader(head) {
		return errors.New("the site's theme set is not a VPK file")
	}
	if err := fsutil.CopyFileAtomic(tmp, filepath.Join(a.store.ModDir(uikitID), uikitSetFile)); err != nil {
		return err
	}
	a.mu.Lock()
	if m := a.state.Mod(uikitID); m != nil {
		m.Set, m.SetGame, m.SetCore, m.SetNext, m.SetNote, m.SetFailed = key, game, core, "", "", ""
	}
	a.saveLocked()
	a.mu.Unlock()
	log.Printf("UI Kit set %s for game %s, UI Kit %s installed", key, game, core)
	return nil
}

// setRebuildDueLocked: UI Kit runs files for game `local` (not parked), the
// player chose a set that isn't installed yet or the set in the store was
// built for another game or core (or the starter set is in place), and the
// site hasn't refused this game and core. mu must be held.
func (a *App) setRebuildDueLocked(local string) bool {
	m := a.state.Mod(uikitID)
	if m == nil || wantedSet(m) == "" || !m.Enabled || m.Parked || a.state.FrogtechDisabled || local == "" || stale(m.Game, local) {
		return false
	}
	if m.SetNext == "" && m.SetGame == local && m.SetCore == m.Version {
		return false
	}
	return m.SetFailed != local+"/"+m.Version
}

// rebuildSet asks the site for the player's set built for game `local` and
// the installed core, and installs it. Run by checkGameBuilds once UI Kit's
// own files are for this game.
func (a *App) rebuildSet(ctx context.Context, local string) error {
	a.mu.Lock()
	due := a.setRebuildDueLocked(local)
	var key, core string
	if due {
		m := a.state.Mod(uikitID)
		key, core = wantedSet(m), m.Version
	}
	a.mu.Unlock()
	if !due {
		return nil
	}
	newKey, err := requestRebuild(ctx, key, local, core)
	var later *errSetLater
	var broken *errSetBroken
	switch {
	case errors.As(err, &later):
		log.Printf("UI Kit set %s: %v; the starter set runs, asking again in %s", key, err, parkedRetryInterval)
		return nil
	case errors.As(err, &broken):
		log.Printf("UI Kit set %s: %v", key, err)
		a.mu.Lock()
		if m := a.state.Mod(uikitID); m != nil && wantedSet(m) == key && m.Version == core {
			markSetBroken(m, local, core)
			a.saveLocked()
		}
		a.mu.Unlock()
		return nil
	case err != nil:
		return err
	}
	defer a.beginOp("Installing your themes for game " + local)()
	a.mu.Lock()
	m := a.state.Mod(uikitID)
	still := m != nil && wantedSet(m) == key && m.Version == core && !m.Parked && m.Enabled && !stale(m.Game, local) &&
		a.clientVersionLocked() == local
	a.mu.Unlock()
	if !still {
		return nil // changed meanwhile; the next check sees it
	}
	if err := a.installSetLocked(ctx, newKey, local, core); err != nil {
		return fmt.Errorf("UI Kit set for game %s: %w", local, err)
	}
	return a.changedLocked()
}

// keepSet carries the player's set over to a new install of UI Kit's files
// (a build for another game, a release): the new files bring the starter
// set, so the set is asked for again for their game and core.
func keepSet(old, m *mods.Mod) {
	if old != nil && m.ID == uikitID {
		m.Set, m.SetNext = old.Set, old.SetNext
	}
}
