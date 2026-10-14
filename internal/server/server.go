// Package server serves the page (web/, embedded) and its JSON API on
// 127.0.0.1, guarded by a token and a Host check, and finds or talks to an
// already running instance.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
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

	"github.com/fr0gtech/frogtech-mod-loader/internal/app"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
	"github.com/fr0gtech/frogtech-mod-loader/internal/icon"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
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

// LoadToken returns the persistent API token, creating it on first use. It is
// kept across restarts so an open page keeps working after a self-update.
func LoadToken(dataDir string) string {
	p := filepath.Join(dataDir, "token")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b))
	}
	t := fsutil.RandomHex(24)
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

func InstancePath(dataDir string) string { return filepath.Join(dataDir, "instance.json") }

// FindRunning returns the base URL and pid of an already running instance.
func FindRunning(dataDir, token string) (string, int) {
	var info instanceInfo
	if b, err := os.ReadFile(InstancePath(dataDir)); err == nil {
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

// ForwardURL hands a frogmod:// link to the running instance.
func ForwardURL(base string, pid int, token, link string) {
	winui.AllowForeground(pid)
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

// ShowRunning asks a running instance to show its window.
func ShowRunning(base string, pid int, token string) {
	winui.AllowForeground(pid)
	if resp, err := localCall(http.MethodPost, base+"/api/show", token); err == nil {
		resp.Body.Close()
	}
}

var (
	srvMu     sync.Mutex
	srvActive *http.Server
)

func Stop() {
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
func Start(a *app.App) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultPort))
	if err != nil {
		log.Printf("port %d busy (%v), using a free port", defaultPort, err)
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	a.SetBaseURL(base)
	b, _ := json.Marshal(instanceInfo{Port: port, PID: os.Getpid()})
	os.WriteFile(InstancePath(a.DataDir()), b, 0o600)
	srv := &http.Server{Handler: routes(a), ReadHeaderTimeout: 10 * time.Second}
	srvMu.Lock()
	srvActive = srv
	srvMu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	}()
	log.Printf("UI at %s", base)
	return nil
}

func routes(a *app.App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { handleIndex(a, w, r) })
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
		w.Write(icon.Favicon())
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"app": "FrogLoader", "version": app.Version, "pid": os.Getpid()})
	})
	api.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.Status()) })
	api.HandleFunc("POST /api/gamedir", jsonOp(func(r *http.Request, b body) error { return a.SetGameDir(b.Path) }))
	api.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s mods.Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&s); err != nil {
			httpErr(w, err)
			return
		}
		reply(w, a.SetSettings(s))
	})
	api.HandleFunc("POST /api/catalog/install", jsonOp(func(r *http.Request, b body) error { return a.InstallCatalogMod(r.Context(), b.ID) }))
	api.HandleFunc("POST /api/mods/update", jsonOp(func(r *http.Request, b body) error { return a.UpdateMod(r.Context(), b.ID) }))
	api.HandleFunc("POST /api/mods/enable", jsonOp(func(r *http.Request, b body) error { return a.SetEnabled(b.ID, b.Enabled) }))
	api.HandleFunc("POST /api/mods/remove", jsonOp(func(r *http.Request, b body) error { return a.Remove(b.ID) }))
	api.HandleFunc("POST /api/mods/order", jsonOp(func(r *http.Request, b body) error { return a.SetOrder(b.IDs) }))
	api.HandleFunc("POST /api/mods/upload", func(w http.ResponseWriter, r *http.Request) { handleUpload(a, w, r) })
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
	api.HandleFunc("POST /api/gamebanana/add", jsonOp(func(r *http.Request, b body) error {
		return a.AddGameBanana(r.Context(), b.URL, b.Model, b.GBID, b.FileID)
	}))
	api.HandleFunc("GET /api/browse", func(w http.ResponseWriter, r *http.Request) { handleBrowse(a, w, r) })
	api.HandleFunc("GET /api/browse/categories", func(w http.ResponseWriter, r *http.Request) {
		cats, err := gamebanana.Categories(r.Context(), r.URL.Query().Get("model"))
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, cats)
	})
	api.HandleFunc("GET /api/browse/item", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.Atoi(q.Get("id"))
		d, err := gamebanana.Details(r.Context(), q.Get("model"), id)
		if err != nil {
			httpErr(w, err)
			return
		}
		if m := a.InstalledGB(d.Model, d.ID); m != nil {
			d.Installed, d.InstalledID = true, m.GBFileID
		}
		writeJSON(w, d)
	})
	api.HandleFunc("POST /api/show", jsonOp(func(r *http.Request, b body) error { a.ShowWindow(); return nil }))
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
		added := a.RepoAdded(rp.ID)
		writeJSON(w, map[string]any{"ok": true, "id": rp.ID, "url": rp.URL, "name": cat.Name, "description": cat.Description,
			"homepage": cat.Homepage, "modCount": len(cat.Mods), "added": added})
	})
	api.HandleFunc("POST /api/repos/add", jsonOp(func(r *http.Request, b body) error { _, err := a.AddRepo(r.Context(), b.URL, b.Key); return err }))
	api.HandleFunc("POST /api/repos/remove", jsonOp(func(r *http.Request, b body) error { return a.RemoveRepo(b.ID) }))
	api.HandleFunc("POST /api/repos/enable", jsonOp(func(r *http.Request, b body) error { return a.SetRepoEnabled(r.Context(), b.ID, b.Enabled) }))
	api.HandleFunc("POST /api/repos/key", jsonOp(func(r *http.Request, b body) error { return a.SetRepoKey(r.Context(), b.ID, b.Key) }))
	api.HandleFunc("POST /api/repos/refresh", jsonOp(func(r *http.Request, b body) error {
		a.RefreshCatalog(r.Context())
		a.RefreshRepos(r.Context())
		return nil
	}))
	api.HandleFunc("POST /api/repo/install", jsonOp(func(r *http.Request, b body) error { return a.InstallRepoMod(r.Context(), b.Repo, b.ID) }))
	api.HandleFunc("POST /api/url", jsonOp(func(r *http.Request, b body) error {
		go a.HandleURL(context.Background(), b.URL)
		return nil
	}))
	api.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.TakeEvents()) })
	api.HandleFunc("POST /api/catalog/refresh", jsonOp(func(r *http.Request, b body) error { return a.RefreshCatalog(r.Context()) }))
	api.HandleFunc("POST /api/settings/dontask", jsonOp(func(r *http.Request, b body) error { return a.SetDontAsk(b.ID, b.Enabled) }))
	api.HandleFunc("POST /api/settings/reset-dontask", jsonOp(func(r *http.Request, b body) error { return a.ResetDontAsk() }))
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
	api.HandleFunc("POST /api/open-external", jsonOp(func(r *http.Request, b body) error {
		u, err := url.Parse(b.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("only web links can be opened")
		}
		return platform.OpenURL(u.String())
	}))
	api.HandleFunc("POST /api/dmm/remove-leftover", jsonOp(func(r *http.Request, b body) error {
		return a.RemoveDMMLeftover()
	}))
	api.HandleFunc("POST /api/apply", jsonOp(func(r *http.Request, b body) error { return a.Apply() }))
	api.HandleFunc("POST /api/import", jsonOp(func(r *http.Request, b body) error { return a.ImportUnknown() }))
	api.HandleFunc("POST /api/fix-gameinfo", jsonOp(func(r *http.Request, b body) error { _, err := a.EnsureGameinfo(); return err }))
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
	api.HandleFunc("POST /api/self-update", jsonOp(func(r *http.Request, b body) error { return a.StartSelfUpdate() }))
	api.HandleFunc("POST /api/play", jsonOp(func(r *http.Request, b body) error { return a.Play(b.Force) }))
	api.HandleFunc("POST /api/open-folder", jsonOp(func(r *http.Request, b body) error {
		dir := a.GameDir()
		if b.ID == "data" {
			dir = a.DataDir()
		} else if dir != "" {
			_, dir, _ = steam.Paths(dir)
		}
		if dir == "" {
			return errors.New("game folder not set")
		}
		return platform.OpenFolder(dir)
	}))
	api.HandleFunc("POST /api/quit", jsonOp(func(r *http.Request, b body) error {
		go func() { time.Sleep(300 * time.Millisecond); a.Quit() }()
		return nil
	}))
	mux.Handle("/api/", guard(a, api))
	mux.Handle("GET /img", guard(a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handleImg(a, w, r) })))
	return hostCheck(mux)
}

// hostCheck rejects requests whose Host isn't loopback (DNS rebinding).
func hostCheck(next http.Handler) http.Handler {
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
func guard(a *app.App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Frog-Token")
		if tok == "" {
			if c, err := r.Cookie(cookieName); err == nil {
				tok = c.Value
			}
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.Token())) != 1 {
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

func handleIndex(a *app.App, w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("t"); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(a.Token())) == 1 {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: a.Token(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
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

func jsonOp(fn func(r *http.Request, b body) error) http.HandlerFunc {
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

func handleUpload(a *app.App, w http.ResponseWriter, r *http.Request) {
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
		tmp, err := a.TempFile("upload-*")
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
	var unk *mods.ErrUnknownPaks
	if errors.As(err, &unk) {
		code = http.StatusConflict
	}
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error(), "gameRunning": errors.Is(err, app.ErrGameRunning)})
}

func reply(w http.ResponseWriter, err error) {
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func handleBrowse(a *app.App, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	cat, _ := strconv.Atoi(q.Get("cat"))
	res, err := gamebanana.Browse(r.Context(), gamebanana.BrowseQuery{Model: q.Get("model"), Sort: q.Get("sort"), Category: cat, Search: q.Get("q"), Page: page})
	if err != nil {
		httpErr(w, err)
		return
	}
	for i := range res.Items {
		res.Items[i].Installed = a.InstalledGB(res.Items[i].Model, res.Items[i].ID) != nil
	}
	writeJSON(w, res)
}
