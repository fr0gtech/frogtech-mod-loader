package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Repositories let other modders ship mods and updates through the loader.
// Frogtech's catalog is the built-in repository ("frogtech"); users add more.
// The format is documented at mods.frogtech.dev/docs/repositories and in the
// README: one JSON document, the catalog schema plus format/name/description/
// homepage/icon. Installed ids are "<repoId>/<modId>".

const builtinRepoID = "frogtech"

// Repo is a user-added repository (state.json).
type Repo struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Key      string    `json:"key,omitempty"`      // DPAPI-protected, base64; never the plain key
	KeyLast4 string    `json:"keyLast4,omitempty"` // for "•••• abcd"
	AddedAt  time.Time `json:"addedAt"`
}

// Repository status, as shown in Settings > Repositories.
const (
	repoOK          = "ok"
	repoKeyRejected = "key"         // 401/403: "Key not accepted"
	repoUnreachable = "unreachable" // network error or other HTTP status
	repoInvalid     = "invalid"     // not a valid repository file
)

type repoState struct {
	cat     *Catalog
	status  string
	err     string
	fetched time.Time
}

var (
	repoModIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	repoIDRe    = regexp.MustCompile(`^[a-z0-9._-]{1,120}$`)
	sha256Re    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// checkRepoURL accepts https URLs, and http only for localhost/127.0.0.1.
func checkRepoURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Opaque != "" {
		return nil, errors.New("not a valid web address")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" {
			return nil, errors.New("must use https (http only works for localhost)")
		}
	default:
		return nil, errors.New("must use https")
	}
	if u.User != nil {
		return nil, errors.New("put the key in the key field, not in the address")
	}
	u.Fragment = ""
	return u, nil
}

// repoIDFor derives a stable repository id: host (with port) plus a hash of
// the path and query, e.g. "mods.example.com-1a2b3c4d".
func repoIDFor(u *url.URL) string {
	host := strings.ToLower(u.Host)
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	sum := sha256.Sum256([]byte(u.EscapedPath() + "?" + u.RawQuery))
	return b.String() + "-" + hex.EncodeToString(sum[:4])
}

// validModID accepts our plain ids and "<repoId>/<modId>".
func validModID(id string) bool {
	repo, mod, ok := strings.Cut(id, "/")
	if !ok {
		return validID(id)
	}
	return repoIDRe.MatchString(repo) && repoModIDRe.MatchString(mod) && !strings.Contains(repo, "..")
}

// ParseRepo validates a third-party repository document.
func ParseRepo(data []byte) (*Catalog, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("wrong field types: %v", err)
	}
	if _, ok := raw["format"]; !ok {
		return nil, errors.New(`missing "format": 1`)
	}
	if c.Format != 1 {
		return nil, fmt.Errorf("format %d isn't supported by this loader (it reads format 1)", c.Format)
	}
	if strings.TrimSpace(c.Name) == "" {
		return nil, errors.New(`missing "name"`)
	}
	if m, ok := raw["mods"]; !ok || string(m) == "null" {
		return nil, errors.New(`missing "mods"`)
	}
	seen := map[string]bool{}
	for i, m := range c.Mods {
		where := fmt.Sprintf("mod %d", i+1)
		if m.ID != "" {
			where = fmt.Sprintf("mod %q", m.ID)
		}
		switch {
		case !repoModIDRe.MatchString(m.ID):
			return nil, fmt.Errorf("%s: id must match ^[a-z0-9_-]{1,64}$", where)
		case seen[m.ID]:
			return nil, fmt.Errorf("%s: id is used twice", where)
		case strings.TrimSpace(m.Name) == "":
			return nil, fmt.Errorf("%s: missing name", where)
		case strings.TrimSpace(m.Version) == "":
			return nil, fmt.Errorf("%s: missing version", where)
		case m.URL == "":
			return nil, fmt.Errorf("%s: missing url", where)
		case !sha256Re.MatchString(m.SHA256):
			return nil, fmt.Errorf("%s: sha256 must be 64 hex characters", where)
		case m.Size < 0:
			return nil, fmt.Errorf("%s: size can't be negative", where)
		}
		if _, err := checkRepoURL(m.URL); err != nil {
			return nil, fmt.Errorf("%s: url %v", where, err)
		}
		seen[m.ID] = true
	}
	c.Loader = LoaderInfo{} // only the built-in repository may offer loader updates
	return &c, nil
}

// repoAuth carries a repository key and the only origin it may be sent to.
type repoAuth struct {
	origin *url.URL
	key    string
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// apply adds "Authorization: Bearer <key>" only for the repository's origin.
func (ra *repoAuth) apply(req *http.Request) {
	if ra == nil || ra.key == "" || ra.origin == nil {
		return
	}
	if sameOrigin(req.URL, ra.origin) {
		req.Header.Set("Authorization", "Bearer "+ra.key)
	}
}

// FetchRepo downloads and validates a repository. status is one of the repo*
// constants; err explains anything but repoOK.
func FetchRepo(ctx context.Context, u *url.URL, key string) (*Catalog, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := httpGetAuth(ctx, u.String(), &repoAuth{origin: u, key: key})
	if err != nil {
		var hs *HTTPStatusError
		if errors.As(err, &hs) && (hs.Code == http.StatusUnauthorized || hs.Code == http.StatusForbidden) {
			return nil, repoKeyRejected, errors.New("key not accepted")
		}
		if errors.As(err, &hs) {
			return nil, repoUnreachable, fmt.Errorf("the server answered HTTP %d", hs.Code)
		}
		return nil, repoUnreachable, fmt.Errorf("can't reach it: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil {
		return nil, repoUnreachable, err
	}
	if len(data) > 4<<20 {
		return nil, repoInvalid, errors.New("the file is larger than 4 MB")
	}
	c, err := ParseRepo(data)
	if err != nil {
		return nil, repoInvalid, err
	}
	return c, repoOK, nil
}

// ---- App ----

func (a *App) repoByID(id string) *Repo {
	for _, r := range a.state.Repos {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (a *App) repoKey(r *Repo) string {
	k, err := openKey(r.Key)
	if err != nil {
		log.Printf("repository %s: can't read its key: %v", r.ID, err)
	}
	return k
}

func (a *App) setRepoState(id string, st *repoState) {
	if a.repoStates == nil {
		a.repoStates = map[string]*repoState{}
	}
	a.repoStates[id] = st
}

// refreshRepo fetches one repository and records the result.
func (a *App) refreshRepo(ctx context.Context, id string) error {
	a.mu.Lock()
	r := a.repoByID(id)
	if r == nil {
		a.mu.Unlock()
		return errors.New("no such repository")
	}
	u, _ := url.Parse(r.URL)
	key := a.repoKey(r)
	a.mu.Unlock()
	cat, status, err := FetchRepo(ctx, u, key)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := &repoState{cat: cat, status: status, fetched: time.Now()}
	if err != nil {
		st.err = err.Error()
		if old := a.repoStates[id]; old != nil && old.cat != nil && status == repoUnreachable {
			st.cat = old.cat // keep showing the last good list while offline
		}
		log.Printf("repository %s: %v", id, err)
	} else {
		if r := a.repoByID(id); r != nil && cat.Name != r.Name {
			r.Name = cat.Name
			a.saveLocked()
		}
		for _, m := range a.state.Mods {
			if cm := cat.Mod(m.RepoMod); m.Source == SourceRepo && m.Repo == id && cm != nil && cm.Image != "" && m.Image != cm.Image {
				m.Image = cm.Image
				a.saveLocked()
			}
		}
	}
	a.setRepoState(id, st)
	return err
}

// RefreshRepos fetches every enabled repository at once; one failing never
// blocks the others. It returns "name: reason" for each that failed.
func (a *App) RefreshRepos(ctx context.Context) []string {
	a.mu.Lock()
	var ids, names []string
	for _, r := range a.state.Repos {
		if r.Enabled {
			ids, names = append(ids, r.ID), append(names, r.Name)
		}
	}
	a.mu.Unlock()
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = a.refreshRepo(ctx, id) }()
	}
	wg.Wait()
	var failed []string
	for i, err := range errs {
		if err != nil {
			failed = append(failed, names[i]+": "+err.Error())
		}
	}
	return failed
}

// PreviewRepo fetches a repository without adding it.
func (a *App) PreviewRepo(ctx context.Context, raw, key string) (*Repo, *Catalog, error) {
	u, err := checkRepoURL(raw)
	if err != nil {
		return nil, nil, err
	}
	key = strings.TrimSpace(key)
	cat, status, err := FetchRepo(ctx, u, key)
	if err != nil {
		if status == repoKeyRejected && key == "" {
			return nil, nil, errors.New("this repository needs a key")
		}
		if status == repoKeyRejected {
			return nil, nil, errors.New("key not accepted")
		}
		return nil, nil, err
	}
	return &Repo{ID: repoIDFor(u), URL: u.String(), Name: cat.Name, Enabled: true}, cat, nil
}

// AddRepo checks and adds a repository (the user confirmed in the page).
func (a *App) AddRepo(ctx context.Context, raw, key string) (*Repo, error) {
	r, cat, err := a.PreviewRepo(ctx, raw, key)
	if err != nil {
		return nil, err
	}
	if r.ID == builtinRepoID {
		return nil, errors.New("that id is reserved")
	}
	if u, _ := url.Parse(catalogURL); u != nil {
		if ru, _ := url.Parse(r.URL); ru != nil && ru.String() == u.String() {
			return nil, errors.New("Frogtech's repository is built in")
		}
	}
	if key = strings.TrimSpace(key); key != "" {
		if r.Key, err = sealKey(key); err != nil {
			return nil, fmt.Errorf("couldn't store the key: %w", err)
		}
		r.KeyLast4 = last4(key)
	}
	r.AddedAt = time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.repoByID(r.ID) != nil {
		return nil, fmt.Errorf("%s is already added", r.Name)
	}
	a.state.Repos = append(a.state.Repos, r)
	a.setRepoState(r.ID, &repoState{cat: cat, status: repoOK, fetched: time.Now()})
	log.Printf("added repository %s (%s)", r.Name, r.URL)
	return r, a.saveLocked()
}

func last4(key string) string {
	if len(key) <= 4 {
		return key
	}
	return key[len(key)-4:]
}

// SetRepoKey changes or (with "") removes a repository's key, then refetches.
func (a *App) SetRepoKey(ctx context.Context, id, key string) error {
	key = strings.TrimSpace(key)
	sealed := ""
	if key != "" {
		var err error
		if sealed, err = sealKey(key); err != nil {
			return fmt.Errorf("couldn't store the key: %w", err)
		}
	}
	a.mu.Lock()
	r := a.repoByID(id)
	if r == nil {
		a.mu.Unlock()
		return errors.New("no such repository")
	}
	r.Key, r.KeyLast4 = sealed, ""
	if key != "" {
		r.KeyLast4 = last4(key)
	}
	a.saveLocked()
	a.mu.Unlock()
	a.refreshRepo(ctx, id)
	return nil
}

// SetRepoEnabled turns a repository (or the built-in one) on or off.
func (a *App) SetRepoEnabled(ctx context.Context, id string, on bool) error {
	a.mu.Lock()
	if id == builtinRepoID {
		a.state.FrogtechDisabled = !on
		defer a.mu.Unlock()
		return a.saveLocked()
	}
	r := a.repoByID(id)
	if r == nil {
		a.mu.Unlock()
		return errors.New("no such repository")
	}
	r.Enabled = on
	a.saveLocked()
	a.mu.Unlock()
	if on {
		a.refreshRepo(ctx, id)
	}
	return nil
}

// RemoveRepo forgets a repository. Its installed mods stay (without updates).
func (a *App) RemoveRepo(id string) error {
	if id == builtinRepoID {
		return errors.New("Frogtech's repository can't be removed; you can turn it off")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.state.Repos[:0]
	found := false
	for _, r := range a.state.Repos {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return errors.New("no such repository")
	}
	a.state.Repos = out
	delete(a.repoStates, id)
	log.Printf("removed repository %s", id)
	return a.saveLocked()
}

// repoMod finds a mod in an enabled repository's last fetched list.
func (a *App) repoMod(repoID, modID string) (*Repo, *CatalogMod, string) {
	r := a.repoByID(repoID)
	if r == nil {
		return nil, nil, ""
	}
	st := a.repoStates[repoID]
	if st == nil || st.cat == nil {
		return r, nil, ""
	}
	return r, st.cat.Mod(modID), a.repoKey(r)
}

// installRepoLocked downloads a repository mod (opMu held). The key goes only
// to the repository's own origin; sha256 is required and checked.
func (a *App) installRepoLocked(ctx context.Context, repoID, modID string) error {
	a.mu.Lock()
	r, cm, key := a.repoMod(repoID, modID)
	var origin *url.URL
	var name string
	if r != nil {
		origin, _ = url.Parse(r.URL)
		name = r.Name
	}
	a.mu.Unlock()
	if r == nil {
		return fmt.Errorf("repository %s isn't added", repoID)
	}
	if cm == nil {
		return fmt.Errorf("%s isn't in %s", modID, name)
	}
	if _, err := checkRepoURL(cm.URL); err != nil {
		return fmt.Errorf("%s: download address %v", cm.Name, err)
	}
	tmp, err := a.tempFile("repo-*.download")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := DownloadAuth(ctx, cm.URL, tmp, Checksum{Algo: "sha256", Hex: cm.SHA256, Size: cm.Size}, &repoAuth{origin: origin, key: key}); err != nil {
		var hs *HTTPStatusError
		if errors.As(err, &hs) && (hs.Code == 401 || hs.Code == 403) {
			return fmt.Errorf("%s: key not accepted for this download", cm.Name)
		}
		return err
	}
	m := &Mod{ID: repoID + "/" + modID, Source: SourceRepo, Repo: repoID, RepoMod: modID, Name: cm.Name, Version: cm.Version,
		Page: cm.Page, Image: cm.Image}
	return a.installFiles(m, tmp, modID+".vpk")
}

// InstallRepoMod installs (or updates) a repository mod and enables it.
func (a *App) InstallRepoMod(ctx context.Context, repoID, modID string) error {
	defer a.beginOp("Installing " + modID)()
	if err := a.installRepoLocked(ctx, repoID, modID); err != nil {
		return err
	}
	a.mu.Lock()
	if m := a.state.Mod(repoID + "/" + modID); m != nil {
		m.Enabled = true
	}
	a.mu.Unlock()
	return a.changedLocked()
}

// knownImage reports whether raw is an image some repository lists (so the
// image cache may fetch it).
func (a *App) knownImage(raw string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	check := func(c *Catalog) bool {
		if c == nil {
			return false
		}
		if c.Icon == raw {
			return true
		}
		for _, m := range c.Mods {
			if m.Image == raw {
				return true
			}
		}
		return false
	}
	if check(a.catalog) {
		return true
	}
	for _, st := range a.repoStates {
		if check(st.cat) {
			return true
		}
	}
	return false
}
