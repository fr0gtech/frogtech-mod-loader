package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/secrets"
)

// Repositories let other modders ship mods and updates through the loader.
// Frogtech's catalog is the built-in repository ("frogtech"); users add more.
// The format (checked in internal/catalog, documented at
// mods.frogtech.dev/docs/repositories) is one JSON document: the catalog
// schema plus format/name/description/homepage/icon. Installed ids are
// "<repoId>/<modId>".

const builtinRepoID = "frogtech"

type repoState struct {
	cat     *catalog.Catalog
	status  string
	err     string
	fetched time.Time
}

func (a *App) repoByID(id string) *mods.Repo {
	for _, r := range a.state.Repos {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (a *App) repoKey(r *mods.Repo) string {
	k, err := secrets.Open(r.Key)
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
	cat, status, err := catalog.FetchRepo(ctx, u, key)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := &repoState{cat: cat, status: status, fetched: time.Now()}
	if err != nil {
		st.err = err.Error()
		if old := a.repoStates[id]; old != nil && old.cat != nil && status == catalog.StatusUnreachable {
			st.cat = old.cat // keep showing the last good list while offline
		}
		log.Printf("repository %s: %v", id, err)
	} else {
		if r := a.repoByID(id); r != nil && cat.Name != r.Name {
			r.Name = cat.Name
			a.saveLocked()
		}
		for _, m := range a.state.Mods {
			if cm := cat.Mod(m.RepoMod); m.Source == mods.SourceRepo && m.Repo == id && cm != nil && cm.Image != "" && m.Image != cm.Image {
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
func (a *App) PreviewRepo(ctx context.Context, raw, key string) (*mods.Repo, *catalog.Catalog, error) {
	u, err := catalog.CheckURL(raw)
	if err != nil {
		return nil, nil, err
	}
	key = strings.TrimSpace(key)
	cat, status, err := catalog.FetchRepo(ctx, u, key)
	if err != nil {
		if status == catalog.StatusKeyRejected && key == "" {
			return nil, nil, errors.New("this repository needs a key")
		}
		if status == catalog.StatusKeyRejected {
			return nil, nil, errors.New("key not accepted")
		}
		return nil, nil, err
	}
	return &mods.Repo{ID: catalog.RepoID(u), URL: u.String(), Name: cat.Name, Enabled: true}, cat, nil
}

// AddRepo checks and adds a repository (the user confirmed in the page).
func (a *App) AddRepo(ctx context.Context, raw, key string) (*mods.Repo, error) {
	r, cat, err := a.PreviewRepo(ctx, raw, key)
	if err != nil {
		return nil, err
	}
	if r.ID == builtinRepoID {
		return nil, errors.New("that id is reserved")
	}
	if u, _ := url.Parse(catalog.FrogtechURL); u != nil {
		if ru, _ := url.Parse(r.URL); ru != nil && ru.String() == u.String() {
			return nil, errors.New("Frogtech's repository is built in")
		}
	}
	if key = strings.TrimSpace(key); key != "" {
		if r.Key, err = secrets.Seal(key); err != nil {
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
	a.setRepoState(r.ID, &repoState{cat: cat, status: catalog.StatusOK, fetched: time.Now()})
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
		if sealed, err = secrets.Seal(key); err != nil {
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
func (a *App) repoMod(repoID, modID string) (*mods.Repo, *catalog.Mod, string) {
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
	if _, err := catalog.CheckURL(cm.URL); err != nil {
		return fmt.Errorf("%s: download address %v", cm.Name, err)
	}
	tmp, err := a.TempFile("repo-*.download")
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := fetch.DownloadAuth(ctx, cm.URL, tmp, fetch.Checksum{Algo: "sha256", Hex: cm.SHA256, Size: cm.Size}, &fetch.Auth{Origin: origin, Key: key}); err != nil {
		var hs *fetch.StatusError
		if errors.As(err, &hs) && (hs.Code == 401 || hs.Code == 403) {
			return fmt.Errorf("%s: key not accepted for this download", cm.Name)
		}
		return err
	}
	m := &mods.Mod{ID: repoID + "/" + modID, Source: mods.SourceRepo, Repo: repoID, RepoMod: modID, Name: cm.Name, Version: cm.Version,
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
	check := func(c *catalog.Catalog) bool {
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
