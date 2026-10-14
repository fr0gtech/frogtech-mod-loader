package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The page and its font (Jost, SIL OFL 1.1; the license text is in
// web/fonts) are embedded so the window works offline.
//
//go:embed web/index.html web/fonts/*.woff2
var webFS embed.FS

const (
	defaultPort = 47621
	cookieName  = "frogloader_token"
	maxUpload   = 2 << 30
)

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// loadToken returns the persistent API token, creating it on first use. It is
// kept across restarts so an open page keeps working after a self-update.
func loadToken(dataDir string) string {
	p := filepath.Join(dataDir, "token")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b))
	}
	t := randomHex(24)
	if err := os.WriteFile(p, []byte(t), 0o600); err != nil {
		log.Printf("token: %v", err)
	}
	return t
}

// instanceInfo lets a second launch find the running instance.
type instanceInfo struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

func instancePath(dataDir string) string { return filepath.Join(dataDir, "instance.json") }

// findRunning returns the base URL and pid of an already running instance.
func findRunning(dataDir, token string) (string, int) {
	var info instanceInfo
	if b, err := os.ReadFile(instancePath(dataDir)); err == nil {
		json.Unmarshal(b, &info)
	}
	for _, p := range []int{info.Port, defaultPort} {
		if p == 0 {
			continue
		}
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		resp, err := localCall(http.MethodGet, base+"/api/ping", token)
		if err != nil {
			continue
		}
		var ping struct {
			PID int `json:"pid"`
		}
		json.NewDecoder(resp.Body).Decode(&ping)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && resp.Header.Get("X-Frog-Loader") != "" {
			return base, ping.PID
		}
	}
	return "", 0
}

func localCall(method, url, token string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Frog-Token", token)
	return (&http.Client{Timeout: 3 * time.Second}).Do(req)
}

// forwardURL hands a frogmod:// link to the running instance.
func forwardURL(base string, pid int, token, link string) {
	allowForeground(pid)
	b, _ := json.Marshal(map[string]string{"url": link})
	req, err := http.NewRequest(http.MethodPost, base+"/api/url", bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("X-Frog-Token", token)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
		resp.Body.Close()
	} else {
		log.Printf("forwarding %s: %v", link, err)
	}
}

// showRunning asks a running instance to show its window.
func showRunning(base string, pid int, token string) {
	allowForeground(pid)
	if resp, err := localCall(http.MethodPost, base+"/api/show", token); err == nil {
		resp.Body.Close()
	}
}

var (
	srvMu     sync.Mutex
	srvActive *http.Server
)

func stopServer() {
	srvMu.Lock()
	defer srvMu.Unlock()
	if srvActive != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srvActive.Shutdown(ctx)
		srvActive = nil
	}
}

// StartServer listens on 127.0.0.1 (fixed port, else any free one).
func (a *App) StartServer() error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultPort))
	if err != nil {
		log.Printf("port %d busy (%v), using a free port", defaultPort, err)
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	a.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	b, _ := json.Marshal(instanceInfo{Port: port, PID: os.Getpid()})
	os.WriteFile(instancePath(a.dataDir), b, 0o600)
	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	srvMu.Lock()
	srvActive = srv
	srvMu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	}()
	log.Printf("UI at %s", a.baseURL)
	return nil
}

// OpenURL is the address the window loads (it carries the token once, which
// the page then keeps in a cookie).
func (a *App) OpenURL() string { return a.baseURL + "/?t=" + a.token }

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("GET /fonts/{name}", func(w http.ResponseWriter, r *http.Request) {
		data, err := webFS.ReadFile("web/fonts/" + r.PathValue("name"))
		if err != nil || !strings.HasSuffix(r.PathValue("name"), ".woff2") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(data)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(iconICO())
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"app": "FrogLoader", "version": version, "pid": os.Getpid()})
	})
	api.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.Status()) })
	api.HandleFunc("POST /api/gamedir", a.jsonOp(func(r *http.Request, b body) error { return a.SetGameDir(b.Path) }))
	api.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&s); err != nil {
			httpErr(w, err)
			return
		}
		reply(w, a.SetSettings(s))
	})
	api.HandleFunc("POST /api/catalog/install", a.jsonOp(func(r *http.Request, b body) error { return a.InstallCatalogMod(r.Context(), b.ID) }))
	api.HandleFunc("POST /api/mods/update", a.jsonOp(func(r *http.Request, b body) error { return a.UpdateMod(r.Context(), b.ID) }))
	api.HandleFunc("POST /api/mods/enable", a.jsonOp(func(r *http.Request, b body) error { return a.SetEnabled(b.ID, b.Enabled) }))
	api.HandleFunc("POST /api/mods/remove", a.jsonOp(func(r *http.Request, b body) error { return a.Remove(b.ID) }))
	api.HandleFunc("POST /api/mods/order", a.jsonOp(func(r *http.Request, b body) error { return a.SetOrder(b.IDs) }))
	api.HandleFunc("POST /api/mods/upload", a.handleUpload)
	api.HandleFunc("POST /api/gamebanana/info", func(w http.ResponseWriter, r *http.Request) {
		var b body
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
			httpErr(w, err)
			return
		}
		m, err := a.GameBananaInfo(r.Context(), b.URL)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"id": m.ID, "name": m.Name, "version": m.Version, "author": m.Submitter.Name, "files": m.ActiveFiles(), "page": m.ProfileURL})
	})
	api.HandleFunc("POST /api/gamebanana/add", a.jsonOp(func(r *http.Request, b body) error {
		return a.AddGameBanana(r.Context(), b.URL, b.Model, b.GBID, b.FileID)
	}))
	api.HandleFunc("GET /api/browse", a.handleBrowse)
	api.HandleFunc("GET /api/browse/categories", func(w http.ResponseWriter, r *http.Request) {
		cats, err := Categories(r.Context(), r.URL.Query().Get("model"))
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, cats)
	})
	api.HandleFunc("GET /api/browse/item", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.Atoi(q.Get("id"))
		d, err := Details(r.Context(), q.Get("model"), id)
		if err != nil {
			httpErr(w, err)
			return
		}
		if m := a.installedGB(d.Model, d.ID); m != nil {
			d.Installed, d.InstalledID = true, m.GBFileID
		}
		writeJSON(w, d)
	})
	api.HandleFunc("POST /api/show", a.jsonOp(func(r *http.Request, b body) error { a.ShowWindow(); return nil }))
	api.HandleFunc("POST /api/repos/preview", func(w http.ResponseWriter, r *http.Request) {
		var b body
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
			httpErr(w, err)
			return
		}
		rp, cat, err := a.PreviewRepo(r.Context(), b.URL, b.Key)
		if err != nil {
			httpErr(w, err)
			return
		}
		a.mu.Lock()
		added := a.repoByID(rp.ID) != nil
		a.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "id": rp.ID, "url": rp.URL, "name": cat.Name, "description": cat.Description,
			"homepage": cat.Homepage, "modCount": len(cat.Mods), "added": added})
	})
	api.HandleFunc("POST /api/repos/add", a.jsonOp(func(r *http.Request, b body) error { _, err := a.AddRepo(r.Context(), b.URL, b.Key); return err }))
	api.HandleFunc("POST /api/repos/remove", a.jsonOp(func(r *http.Request, b body) error { return a.RemoveRepo(b.ID) }))
	api.HandleFunc("POST /api/repos/enable", a.jsonOp(func(r *http.Request, b body) error { return a.SetRepoEnabled(r.Context(), b.ID, b.Enabled) }))
	api.HandleFunc("POST /api/repos/key", a.jsonOp(func(r *http.Request, b body) error { return a.SetRepoKey(r.Context(), b.ID, b.Key) }))
	api.HandleFunc("POST /api/repos/refresh", a.jsonOp(func(r *http.Request, b body) error {
		a.RefreshCatalog(r.Context())
		a.RefreshRepos(r.Context())
		return nil
	}))
	api.HandleFunc("POST /api/repo/install", a.jsonOp(func(r *http.Request, b body) error { return a.InstallRepoMod(r.Context(), b.Repo, b.ID) }))
	api.HandleFunc("POST /api/url", a.jsonOp(func(r *http.Request, b body) error {
		go a.HandleURL(context.Background(), b.URL)
		return nil
	}))
	api.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.TakeEvents()) })
	api.HandleFunc("POST /api/catalog/refresh", a.jsonOp(func(r *http.Request, b body) error { return a.RefreshCatalog(r.Context()) }))
	api.HandleFunc("POST /api/settings/dontask", a.jsonOp(func(r *http.Request, b body) error { return a.SetDontAsk(b.ID, b.Enabled) }))
	api.HandleFunc("POST /api/settings/reset-dontask", a.jsonOp(func(r *http.Request, b body) error { return a.ResetDontAsk() }))
	api.HandleFunc("POST /api/mods/bulk", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			IDs    []string `json:"ids"`
			Action string   `json:"action"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
			httpErr(w, err)
			return
		}
		n, err := a.BulkMods(b.IDs, b.Action)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "count": n})
	})
	api.HandleFunc("POST /api/foreign/delete", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.DeleteForeign()
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "count": n})
	})
	api.HandleFunc("POST /api/open-external", a.jsonOp(func(r *http.Request, b body) error {
		u, err := url.Parse(b.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("only web links can be opened")
		}
		return openURL(u.String())
	}))
	api.HandleFunc("POST /api/dmm/remove-leftover", a.jsonOp(func(r *http.Request, b body) error {
		a.mu.Lock()
		dir := a.state.GameDir
		a.mu.Unlock()
		_, addons, _ := gamePaths(dir)
		return RemoveDMMLeftover(addons)
	}))
	api.HandleFunc("POST /api/apply", a.jsonOp(func(r *http.Request, b body) error { return a.Apply() }))
	api.HandleFunc("POST /api/import", a.jsonOp(func(r *http.Request, b body) error { return a.ImportUnknown() }))
	api.HandleFunc("POST /api/fix-gameinfo", a.jsonOp(func(r *http.Request, b body) error { _, err := a.EnsureGameinfo(); return err }))
	api.HandleFunc("POST /api/check", func(w http.ResponseWriter, r *http.Request) {
		res, err := a.CheckUpdates(r.Context(), false)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "result": res})
	})
	api.HandleFunc("POST /api/mods/update-all", func(w http.ResponseWriter, r *http.Request) {
		installed, err := a.UpdateAll(r.Context())
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "installed": installed})
	})
	api.HandleFunc("POST /api/self-update", a.jsonOp(func(r *http.Request, b body) error {
		c := a.loaderUpdate()
		if c == nil {
			return errors.New("no loader update available")
		}
		if isGameRunning() {
			return ErrGameRunning
		}
		go func() {
			if err := a.SelfUpdate(context.Background(), c); err != nil {
				log.Printf("self-update: %v", err)
				a.toastEvent("error", "Couldn't update the loader: "+err.Error()+". You're still on "+version+".")
			}
		}()
		return nil
	}))
	api.HandleFunc("POST /api/play", a.jsonOp(func(r *http.Request, b body) error { return a.Play(b.Force) }))
	api.HandleFunc("POST /api/open-folder", a.jsonOp(func(r *http.Request, b body) error {
		dir := a.gameDir()
		if b.ID == "data" {
			dir = a.dataDir
		} else if dir != "" {
			_, dir, _ = gamePaths(dir)
		}
		if dir == "" {
			return errors.New("game folder not set")
		}
		return openFolder(dir)
	}))
	api.HandleFunc("POST /api/quit", a.jsonOp(func(r *http.Request, b body) error {
		go func() { time.Sleep(300 * time.Millisecond); a.Quit() }()
		return nil
	}))
	mux.Handle("/api/", a.guard(api))
	mux.Handle("GET /img", a.guard(http.HandlerFunc(a.handleImg)))
	return a.hostCheck(mux)
}

// hostCheck rejects requests whose Host isn't loopback (DNS rebinding).
func (a *App) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && !strings.EqualFold(host, "localhost") {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Frog-Loader", "1")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// guard requires the token (cookie or header) and a same-origin request.
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Frog-Token")
		if tok == "" {
			if c, err := r.Cookie(cookieName); err == nil {
				tok = c.Value
			}
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.token)) != 1 {
			http.Error(w, "unauthorized: open the loader from FrogLoader.exe", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("t"); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(a.token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: a.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	data, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data: https://images.gamebanana.com; font-src 'self'; media-src https://files.gamebanana.com; connect-src 'self'")
	w.Write(data)
}

type body struct {
	ID      string   `json:"id"`
	IDs     []string `json:"ids"`
	Enabled bool     `json:"enabled"`
	Path    string   `json:"path"`
	URL     string   `json:"url"`
	FileID  int      `json:"fileId"`
	Model   string   `json:"model"`
	GBID    int      `json:"gbId"`
	Key     string   `json:"key"`
	Repo    string   `json:"repo"`
	Force   bool     `json:"force"`
}

func (a *App) jsonOp(fn func(r *http.Request, b body) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b body
		if r.ContentLength != 0 {
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
				httpErr(w, err)
				return
			}
		}
		reply(w, fn(r, b))
	}
}

func (a *App) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		httpErr(w, err)
		return
	}
	var added int
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			httpErr(w, err)
			return
		}
		name := part.FileName()
		if name == "" {
			continue
		}
		low := strings.ToLower(name)
		if !strings.HasSuffix(low, ".zip") && !strings.HasSuffix(low, ".vpk") && !strings.HasSuffix(low, ".rar") && !strings.HasSuffix(low, ".7z") {
			httpErr(w, fmt.Errorf("%s: only .zip, .rar, .7z and .vpk files can be added", name))
			return
		}
		tmp, err := a.tempFile("upload-*")
		if err != nil {
			httpErr(w, err)
			return
		}
		f, err := os.Create(tmp)
		if err == nil {
			_, err = io.Copy(f, part)
			f.Close()
		}
		if err == nil {
			err = a.AddLocal(tmp, name)
		}
		os.Remove(tmp)
		if err != nil {
			httpErr(w, fmt.Errorf("%s: %w", name, err))
			return
		}
		added++
	}
	writeJSON(w, map[string]any{"ok": true, "added": added})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	code := http.StatusBadRequest
	var unk *ErrUnknownPaks
	if errors.As(err, &unk) {
		code = http.StatusConflict
	}
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error(), "gameRunning": errors.Is(err, ErrGameRunning)})
}

func reply(w http.ResponseWriter, err error) {
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleBrowse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	cat, _ := strconv.Atoi(q.Get("cat"))
	res, err := Browse(r.Context(), BrowseQuery{Model: q.Get("model"), Sort: q.Get("sort"), Category: cat, Search: q.Get("q"), Page: page})
	if err != nil {
		httpErr(w, err)
		return
	}
	for i := range res.Items {
		res.Items[i].Installed = a.installedGB(res.Items[i].Model, res.Items[i].ID) != nil
	}
	writeJSON(w, res)
}

// installedGB returns a copy of the installed mod for a GameBanana item.
func (a *App) installedGB(model string, id int) *Mod {
	a.mu.Lock()
	defer a.mu.Unlock()
	if m := a.state.Mod(gbModID(model, id)); m != nil {
		c := *m
		return &c
	}
	return nil
}

// ---- status ----

type modView struct {
	*Mod
	Update    string   `json:"update,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"` // enabled mods this one conflicts with
}

type catalogView struct {
	CatalogMod
	FullID       string `json:"fullId"` // installed mod id: "<id>" (Frogtech) or "<repoId>/<id>"
	Installed    string `json:"installed,omitempty"`
	Update       bool   `json:"update"`
	GameMismatch bool   `json:"gameMismatch"`
}

type statusView struct {
	Version      string        `json:"version"`
	Game         GameInfo      `json:"game"`
	GameFound    bool          `json:"gameFound"`
	GameRunning  bool          `json:"gameRunning"`
	GameinfoOK   bool          `json:"gameinfoOk"`
	GameinfoErr  string        `json:"gameinfoErr,omitempty"`
	AddonsDir    string        `json:"addonsDir,omitempty"`
	DMM          bool          `json:"dmm"`
	DMMLeftover  bool          `json:"dmmLeftover"`
	UnknownPaks  []string      `json:"unknownPaks"`
	PendingApply bool          `json:"pendingApply"`
	AppliedOnce  bool          `json:"appliedOnce"`
	Settings     Settings      `json:"settings"`
	RunKey       bool          `json:"runKey"`
	Mods         []modView     `json:"mods"`
	Catalog      []catalogView `json:"catalog"`
	Repos        []repoView    `json:"repos"` // Frogtech first, then the user's repositories
	CatalogErr   string        `json:"catalogErr,omitempty"`
	LoaderUpdate string        `json:"loaderUpdate,omitempty"`
	LoaderNotes  []string      `json:"loaderNotes,omitempty"`
	LastCheck    time.Time     `json:"lastCheck"`
	LastErr      string        `json:"lastErr,omitempty"`
	Busy         string        `json:"busy,omitempty"`
	DataDir      string        `json:"dataDir"`
	Log          []string      `json:"log"`
}

// Status gathers everything the page shows.
func (a *App) Status() statusView {
	ups := map[string]string{}
	for _, u := range a.pendingUpdates() {
		ups[u.modID] = u.label
	}
	var loaderUp string
	var loaderNotes []string
	if c := a.loaderUpdate(); c != nil {
		loaderUp, loaderNotes = c.Loader.Version, c.Loader.Notes
	}
	running := isGameRunning()

	a.mu.Lock()
	defer a.mu.Unlock()
	s := statusView{
		Version: version, GameRunning: running, PendingApply: a.state.PendingApply, AppliedOnce: a.state.AppliedOnce,
		Settings: a.state.Settings, RunKey: getRunAtStartup(), CatalogErr: a.catalogErr, LoaderUpdate: loaderUp, LoaderNotes: loaderNotes,
		LastCheck: a.lastCheck, LastErr: a.lastErr, Busy: a.busy, DataDir: a.dataDir, Log: logTail(),
		UnknownPaks: []string{}, Mods: []modView{}, Catalog: []catalogView{},
	}
	if dir := a.state.GameDir; dir != "" && IsGameDir(dir) {
		s.GameFound = true
		s.Game = ReadGameInfo(dir)
		ok, err := GameinfoStatus(dir)
		s.GameinfoOK = ok
		if err != nil {
			s.GameinfoErr = err.Error()
		}
		_, addons, _ := gamePaths(dir)
		s.AddonsDir = addons
		s.DMM, s.DMMLeftover = DMMState(addons)
		if u, err := UnknownPaks(addons, a.state.Applied); err == nil && u != nil {
			s.UnknownPaks = u
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
	view := func(c *Catalog, prefix string, tags bool) []catalogView {
		out := []catalogView{}
		if c == nil {
			return out
		}
		for _, cm := range c.Mods {
			v := catalogView{CatalogMod: cm, FullID: prefix + cm.ID}
			if tags {
				v.Tags = cm.TagsOf()
			}
			if m := a.state.Mod(v.FullID); m != nil {
				v.Installed = m.Version
				v.Update = CompareVersions(cm.Version, m.Version) > 0
			}
			v.GameMismatch = cm.Game != "" && s.Game.ClientVersion != "" && cm.Game != s.Game.ClientVersion
			out = append(out, v)
		}
		return out
	}
	s.Catalog = view(a.catalog, "", true)
	fv := repoView{ID: builtinRepoID, Name: "Frogtech", URL: catalogURL, Builtin: true, Enabled: !a.state.FrogtechDisabled,
		Status: repoOK, Mods: s.Catalog, ModCount: len(s.Catalog), Fetched: a.catalogAt, Homepage: "https://mods.frogtech.dev"}
	if a.catalog == nil {
		fv.Status, fv.Error = "", a.catalogErr
		if a.catalogErr != "" {
			fv.Status = repoUnreachable
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

var uiLog = &logRing{}

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
	uiLog.mu.Lock()
	defer uiLog.mu.Unlock()
	return append([]string(nil), uiLog.lines...)
}
