package app

import (
	"strings"
	"sync"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gameinfo"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
)

// OpenURL is the address the window loads (it carries the token once, which
// the page then keeps in a cookie).
func (a *App) OpenURL() string { return a.baseURL + "/?t=" + a.token }

// InstalledGB returns a copy of the installed mod for a GameBanana item.
func (a *App) InstalledGB(model string, id int) *mods.Mod {
	a.mu.Lock()
	defer a.mu.Unlock()
	if m := a.state.Mod(gamebanana.ModID(model, id)); m != nil {
		c := *m
		return &c
	}
	return nil
}

// ---- status ----

type modView struct {
	*mods.Mod
	Update    string   `json:"update,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"` // enabled mods this one conflicts with
}

type catalogView struct {
	catalog.Mod
	FullID       string `json:"fullId"` // installed mod id: "<id>" (Frogtech) or "<repoId>/<id>"
	Installed    string `json:"installed,omitempty"`
	Update       bool   `json:"update"`
	GameMismatch bool   `json:"gameMismatch"`
}

type statusView struct {
	Version     string         `json:"version"`
	Game        steam.GameInfo `json:"game"`
	GameFound   bool           `json:"gameFound"`
	GameRunning bool           `json:"gameRunning"`
	// GameinfoOK: gameinfo.gi is as the switches want it (patched with mods
	// on, Valve's with mods off).
	GameinfoOK  bool            `json:"gameinfoOk"`
	GameinfoErr string          `json:"gameinfoErr,omitempty"`
	Gameinfo    gameinfo.Report `json:"gameinfo"`
	ModsOn      bool            `json:"modsOn"`
	StartFail   *StartFail      `json:"startFail,omitempty"`
	Repair      *repairView     `json:"repair,omitempty"`
	NoModsRun   bool            `json:"noModsRun"`
	AddonsDir   string          `json:"addonsDir,omitempty"`
	DMM         bool            `json:"dmm"`
	DMMLeftover bool            `json:"dmmLeftover"`
	UnknownPaks []string        `json:"unknownPaks"`
	// UnknownDetails explains each foreign pak (size, time, contents, what it replaced).
	UnknownDetails []mods.UnknownPak `json:"unknownDetails"`
	PendingApply   bool              `json:"pendingApply"`
	AppliedOnce    bool              `json:"appliedOnce"`
	Settings       mods.Settings     `json:"settings"`
	RunKey         bool              `json:"runKey"`
	Mods           []modView         `json:"mods"`
	Catalog        []catalogView     `json:"catalog"`
	Repos          []repoView        `json:"repos"` // Frogtech first, then the user's repositories
	CatalogErr     string            `json:"catalogErr,omitempty"`
	LoaderUpdate   string            `json:"loaderUpdate,omitempty"`
	LoaderNotes    []string          `json:"loaderNotes,omitempty"`
	LastCheck      time.Time         `json:"lastCheck"`
	LastErr        string            `json:"lastErr,omitempty"`
	Busy           string            `json:"busy,omitempty"`
	DataDir        string            `json:"dataDir"`
	Log            []string          `json:"log"`
}

// Status gathers everything the page shows.
func (a *App) Status() statusView {
	ups := map[string]string{}
	for _, u := range a.pendingUpdates() {
		ups[u.modID] = u.label
	}
	var loaderUp string
	var loaderNotes []string
	if c := a.LoaderUpdate(); c != nil {
		loaderUp, loaderNotes = c.Loader.Version, c.Loader.Notes
	}
	running := gameRunning()

	a.mu.Lock()
	defer a.mu.Unlock()
	s := statusView{
		Version: Version, GameRunning: running, PendingApply: a.state.PendingApply, AppliedOnce: a.state.AppliedOnce,
		Settings: a.state.Settings, RunKey: platform.GetRunAtStartup(), CatalogErr: a.catalogErr, LoaderUpdate: loaderUp, LoaderNotes: loaderNotes,
		LastCheck: a.lastCheck, LastErr: a.lastErr, Busy: a.busy, DataDir: a.dataDir, Log: logTail(),
		UnknownPaks: []string{}, UnknownDetails: []mods.UnknownPak{}, Mods: []modView{}, Catalog: []catalogView{},
		ModsOn: !a.state.ModsOff, StartFail: a.startFail, NoModsRun: a.state.NoModsRun != nil,
	}
	if r := a.state.Repair; r != nil {
		s.Repair = &repairView{Started: r.Started, Checking: r.SawCheck}
	}
	if dir := a.state.GameDir; dir != "" && steam.IsGameDir(dir) {
		s.GameFound = true
		s.Game = steam.ReadGameInfo(dir)
		r, err := gameinfo.Status(dir)
		s.Gameinfo = r
		if a.modsOffLocked() {
			s.GameinfoOK = err == nil && !r.Addons
		} else {
			s.GameinfoOK = r.Patched()
		}
		if err != nil {
			s.GameinfoErr = err.Error()
		}
		_, addons, _ := steam.Paths(dir)
		s.AddonsDir = addons
		s.DMM, s.DMMLeftover = mods.DMMState(addons)
		if u, err := mods.DescribeUnknown(addons, a.state); err == nil {
			s.UnknownDetails = u
			for _, d := range u {
				s.UnknownPaks = append(s.UnknownPaks, d.Name)
			}
		}
	} else {
		s.Game.Dir = a.state.GameDir
	}
	enabled := map[string]bool{}
	for _, m := range a.state.Mods {
		if m.Enabled {
			enabled[m.ID] = true
		}
	}
	for _, m := range a.state.Mods {
		v := modView{Mod: m, Update: ups[m.ID]}
		if cm := a.catalog.Mod(m.ID); cm != nil && m.Enabled {
			for _, c := range cm.Conflicts {
				if enabled[c] {
					v.Conflicts = append(v.Conflicts, c)
				}
			}
		}
		s.Mods = append(s.Mods, v)
	}
	view := func(c *catalog.Catalog, prefix string, tags bool) []catalogView {
		out := []catalogView{}
		if c == nil {
			return out
		}
		for _, cm := range c.Mods {
			v := catalogView{Mod: cm, FullID: prefix + cm.ID}
			if tags {
				v.Tags = cm.TagsOf()
			}
			if m := a.state.Mod(v.FullID); m != nil {
				v.Installed = m.Version
				v.Update = catalog.CompareVersions(cm.Version, m.Version) > 0
			}
			v.GameMismatch = cm.Game != "" && s.Game.ClientVersion != "" && cm.Game != s.Game.ClientVersion
			out = append(out, v)
		}
		return out
	}
	s.Catalog = view(a.catalog, "", true)
	fv := repoView{ID: builtinRepoID, Name: "Frogtech", URL: catalog.FrogtechURL, Builtin: true, Enabled: !a.state.FrogtechDisabled,
		Status: catalog.StatusOK, Mods: s.Catalog, ModCount: len(s.Catalog), Fetched: a.catalogAt, Homepage: "https://mods.frogtech.dev"}
	if a.catalog == nil {
		fv.Status, fv.Error = "", a.catalogErr
		if a.catalogErr != "" {
			fv.Status = catalog.StatusUnreachable
		}
	}
	s.Repos = append(s.Repos, fv)
	for _, r := range a.state.Repos {
		rv := repoView{ID: r.ID, Name: r.Name, URL: r.URL, Enabled: r.Enabled, HasKey: r.Key != "", KeyLast4: r.KeyLast4, Mods: []catalogView{}}
		if st := a.repoStates[r.ID]; st != nil {
			rv.Status, rv.Error, rv.Fetched = st.status, st.err, st.fetched
			rv.Mods = view(st.cat, r.ID+"/", false)
			if st.cat != nil {
				rv.Description, rv.Homepage, rv.Icon = st.cat.Description, st.cat.Homepage, st.cat.Icon
			}
		}
		rv.ModCount = len(rv.Mods)
		s.Repos = append(s.Repos, rv)
	}
	return s
}

type repoView struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	URL         string        `json:"url"`
	Builtin     bool          `json:"builtin"`
	Enabled     bool          `json:"enabled"`
	Status      string        `json:"status"` // ok, key, unreachable, invalid, "" (not fetched yet)
	Error       string        `json:"error,omitempty"`
	Fetched     time.Time     `json:"fetched"`
	ModCount    int           `json:"modCount"`
	HasKey      bool          `json:"hasKey"`
	KeyLast4    string        `json:"keyLast4,omitempty"`
	Description string        `json:"description,omitempty"`
	Homepage    string        `json:"homepage,omitempty"`
	Icon        string        `json:"icon,omitempty"`
	Mods        []catalogView `json:"mods"`
}

// ---- log ring buffer (shown in the UI) ----

type logRing struct {
	mu    sync.Mutex
	lines []string
	part  string
}

var UILog = &logRing{}

func (l *logRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.part += string(p)
	for {
		i := strings.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		l.lines = append(l.lines, l.part[:i])
		l.part = l.part[i+1:]
	}
	if len(l.lines) > 80 {
		l.lines = l.lines[len(l.lines)-80:]
	}
	return len(p), nil
}

func logTail() []string {
	UILog.mu.Lock()
	defer UILog.mu.Unlock()
	return append([]string(nil), UILog.lines...)
}
