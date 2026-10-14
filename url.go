package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// frogmod:// links let the website hand work to the loader:
//
//	frogmod://install/<catalog id>          install or update a Frogtech mod and enable it
//	frogmod://gamebanana/<mod id>           open a GameBanana mod's details in Browse
//	frogmod://gamebanana/sound/<sound id>   same for a GameBanana sound
//
// Windows starts `"<exe>" --url "<link>"`; a second instance forwards the link
// to the running one (POST /api/url) and exits.

const urlScheme = "frogmod"

// FrogURL is a parsed frogmod:// link.
type FrogURL struct {
	Action string // "install", "gamebanana" or "repo-add"
	Repo   string // install: repository id ("" = Frogtech)
	ID     string // install: mod id
	URL    string // repo-add: the repository address
	Model  string // ModelMod or ModelSound (gamebanana)
	GBID   int
}

// ParseFrogURL validates a frogmod:// link.
func ParseFrogURL(raw string) (FrogURL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 512 {
		return FrogURL{}, errors.New("link too long")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, urlScheme) {
		return FrogURL{}, fmt.Errorf("not a %s:// link: %q", urlScheme, raw)
	}
	// frogmod://install/uikit parses as host "install", path "/uikit".
	// Also accept frogmod:install/uikit and trailing slashes.
	parts := []string{}
	for _, p := range strings.Split(strings.Trim(u.Host+"/"+strings.TrimPrefix(u.Opaque, "//")+u.Path, "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return FrogURL{}, errors.New("empty frogmod:// link")
	}
	switch strings.ToLower(parts[0]) {
	case "install":
		switch {
		case len(parts) == 2 && validID(parts[1]):
			return FrogURL{Action: "install", ID: parts[1]}, nil
		case len(parts) == 3 && repoIDRe.MatchString(parts[1]) && !strings.Contains(parts[1], "..") && repoModIDRe.MatchString(parts[2]):
			return FrogURL{Action: "install", Repo: parts[1], ID: parts[2]}, nil
		}
		return FrogURL{}, fmt.Errorf("bad install link %q", raw)
	case "repo":
		if len(parts) != 2 || !strings.EqualFold(parts[1], "add") {
			return FrogURL{}, fmt.Errorf("bad repository link %q", raw)
		}
		ru, err := checkRepoURL(u.Query().Get("url"))
		if err != nil {
			return FrogURL{}, fmt.Errorf("repository link: address %v", err)
		}
		return FrogURL{Action: "repo-add", URL: ru.String()}, nil
	case "gamebanana", "gb":
		model := ModelMod
		rest := parts[1:]
		if len(rest) == 2 {
			switch strings.ToLower(rest[0]) {
			case "sound", "sounds":
				model = ModelSound
			case "mod", "mods":
			default:
				return FrogURL{}, fmt.Errorf("bad GameBanana link %q", raw)
			}
			rest = rest[1:]
		}
		if len(rest) != 1 {
			return FrogURL{}, fmt.Errorf("bad GameBanana link %q", raw)
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil || id <= 0 {
			return FrogURL{}, fmt.Errorf("bad GameBanana id in %q", raw)
		}
		return FrogURL{Action: "gamebanana", Model: model, GBID: id}, nil
	}
	return FrogURL{}, fmt.Errorf("unknown frogmod:// action %q", parts[0])
}

// protocolCommand is the shell\open\command registered for frogmod://.
func protocolCommand(exe string) string {
	return fmt.Sprintf(`"%s" --url "%%1"`, exe)
}

// ---- events for the page (toasts, "open this mod") ----

// Event is something the page should show once.
type Event struct {
	Seq   int    `json:"seq"`
	Kind  string `json:"kind"`  // "toast" or "open"
	Level string `json:"level"` // ok, info, error (toast)
	Text  string `json:"text,omitempty"`
	Model string `json:"model,omitempty"` // open
	GBID  int    `json:"gbId,omitempty"`  // open
}

func (a *App) pushEvent(e Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.eventSeq++
	e.Seq = a.eventSeq
	a.events = append(a.events, e)
	if len(a.events) > 20 {
		a.events = a.events[len(a.events)-20:]
	}
}

func (a *App) toastEvent(level, text string) {
	a.pushEvent(Event{Kind: "toast", Level: level, Text: text})
}

// TakeEvents returns pending events and clears them (the page polls this).
func (a *App) TakeEvents() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	ev := a.events
	a.events = nil
	if ev == nil {
		ev = []Event{}
	}
	return ev
}

// HandleURL runs a frogmod:// link: it brings the window up and reports the
// result as a toast. It never panics on bad input.
func (a *App) HandleURL(ctx context.Context, raw string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("frogmod link %q: panic %v", raw, r)
			a.toastEvent("error", "That link couldn't be handled.")
		}
	}()
	log.Printf("frogmod link: %s", raw)
	a.ShowWindow()
	fu, err := ParseFrogURL(raw)
	if err != nil {
		a.toastEvent("error", "Unknown link: "+err.Error())
		return
	}
	switch fu.Action {
	case "gamebanana":
		a.pushEvent(Event{Kind: "open", Model: fu.Model, GBID: fu.GBID})
	case "repo-add":
		// Never added silently: the page opens the Add repository dialog, prefilled.
		a.pushEvent(Event{Kind: "repo-add", Text: fu.URL})
	case "install":
		if fu.Repo != "" {
			a.mu.Lock()
			r := a.repoByID(fu.Repo)
			a.mu.Unlock()
			if r == nil {
				a.toastEvent("error", "This mod comes from a repository you haven't added yet ("+fu.Repo+"). Get the repository's link from its author and add it in Settings > Repositories first.")
				return
			}
			a.toastEvent("info", "Installing "+fu.ID+" from "+r.Name+"…")
			if err := a.refreshRepo(ctx, fu.Repo); err != nil {
				a.toastEvent("error", r.Name+": "+err.Error())
				return
			}
			if err := a.InstallRepoMod(ctx, fu.Repo, fu.ID); err != nil {
				a.toastEvent("error", err.Error())
				return
			}
			a.mu.Lock()
			name := fu.ID
			if m := a.state.Mod(fu.Repo + "/" + fu.ID); m != nil {
				name = strings.TrimSpace(m.Name + " " + m.Version)
			}
			a.mu.Unlock()
			a.toastEvent("ok", name+" installed from "+r.Name+".")
			return
		}
		a.toastEvent("info", "Installing "+fu.ID+"…")
		msg, err := a.installFromLink(ctx, fu.ID)
		if err != nil {
			a.toastEvent("error", err.Error())
			return
		}
		a.toastEvent("ok", msg)
	}
}

// installFromLink installs or updates a catalog mod and enables it.
func (a *App) installFromLink(ctx context.Context, id string) (string, error) {
	a.mu.Lock()
	haveCatalog := a.catalog != nil && a.catalog.Mod(id) != nil
	a.mu.Unlock()
	if !haveCatalog {
		if err := a.RefreshCatalog(ctx); err != nil {
			return "", fmt.Errorf("couldn't load the Frogtech mod list: %w", err)
		}
	}
	a.mu.Lock()
	cm := a.catalog.Mod(id)
	var cur *Mod
	if m := a.state.Mod(id); m != nil {
		c := *m
		cur = &c
	}
	a.mu.Unlock()
	if cm == nil {
		return "", fmt.Errorf("%q is not a Frogtech mod", id)
	}
	name := cm.Name + " " + cm.Version
	done := a.beginOp("Installing " + cm.Name)
	defer done()
	verb := "installed"
	if cur != nil && CompareVersions(cm.Version, cur.Version) <= 0 {
		verb = "is already installed"
	} else {
		if cur != nil {
			verb = "updated"
		}
		if err := a.installCatalogLocked(ctx, id); err != nil {
			return "", fmt.Errorf("%s: %w", cm.Name, err)
		}
	}
	a.mu.Lock()
	if m := a.state.Mod(id); m != nil && !m.Enabled {
		m.Enabled = true
		if verb == "is already installed" {
			verb = "enabled"
		}
	}
	a.mu.Unlock()
	if err := a.changedLocked(); err != nil {
		return "", err
	}
	if isGameRunning() {
		return name + " " + verb + ". It will be applied when you close Deadlock.", nil
	}
	a.mu.Lock()
	pending := a.state.PendingApply
	a.mu.Unlock()
	if pending {
		return name + " " + verb + ". It goes into the game once the foreign pak files are imported or deleted.", nil
	}
	return name + " " + verb + ".", nil
}

// RefreshCatalog fetches the catalog (no installs) and back-fills images of
// installed catalog mods.
func (a *App) RefreshCatalog(ctx context.Context) error {
	cat, err := FetchCatalog(ctx, catalogURL)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if a.catalog == nil {
			a.catalogErr = err.Error()
		}
		return err
	}
	a.catalog, a.catalogErr, a.catalogAt = cat, "", time.Now()
	changed := false
	for _, m := range a.state.Mods {
		if cm := cat.Mod(m.ID); m.Source == SourceCatalog && cm != nil && cm.Image != "" && m.Image != cm.Image {
			m.Image, changed = cm.Image, true
		}
	}
	if changed {
		a.saveLocked()
	}
	return nil
}
