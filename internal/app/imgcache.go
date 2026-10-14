package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fsutil"
)

// Preview images of installed and Frogtech mods are served through
// /img?u=<url> and kept in %LOCALAPPDATA%\FrogLoader\cache, so the installed
// list doesn't refetch them. Only images from GameBanana and the Frogtech
// site are proxied.

const (
	maxImageBytes  = 8 << 20
	maxCachedFiles = 400
)

func imageHostAllowed(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "images.gamebanana.com" || h == "mods.frogtech.dev" {
		return true
	}
	if c, err := url.Parse(catalog.FrogtechURL); err == nil && strings.EqualFold(c.Hostname(), h) {
		return true // the catalog's own host (tests, mirrors)
	}
	return false
}

// repoImageURL: a repository's images must be https (http only on localhost).
func repoImageURL(u *url.URL) bool {
	_, err := catalog.CheckURL(u.String())
	return err == nil
}

func (a *App) cacheDir() string { return filepath.Join(a.dataDir, "cache") }

func cacheName(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16]) + ".img"
}

// CachedImage returns the image bytes, downloading them on first use.
func (a *App) CachedImage(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || !(imageHostAllowed(u) || (a.knownImage(raw) && repoImageURL(u))) {
		return nil, errors.New("image host not allowed")
	}
	p := filepath.Join(a.cacheDir(), cacheName(raw))
	if b, err := os.ReadFile(p); err == nil {
		return b, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch.Get(ctx, raw)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImageBytes {
		return nil, errors.New("image too large")
	}
	if !strings.HasPrefix(http.DetectContentType(b), "image/") {
		return nil, errors.New("not an image")
	}
	if err := os.MkdirAll(a.cacheDir(), 0o755); err == nil {
		if fsutil.WriteFileAtomic(p, b) == nil {
			pruneCache(a.cacheDir())
		}
	}
	return b, nil
}

// pruneCache keeps the newest maxCachedFiles images.
func pruneCache(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= maxCachedFiles {
		return
	}
	type f struct {
		name string
		t    time.Time
	}
	var files []f
	for _, e := range ents {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			files = append(files, f{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].t.After(files[j].t) })
	for _, x := range files[min(len(files), maxCachedFiles):] {
		os.Remove(filepath.Join(dir, x.name))
	}
}
