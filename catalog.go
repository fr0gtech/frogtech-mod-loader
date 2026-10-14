package main

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var catalogURL = "https://mods.frogtech.dev/loader/catalog.json"

// Catalog is the list of our own mods plus the latest loader build.
type Catalog struct {
	Loader LoaderInfo   `json:"loader"` // honoured only from the built-in Frogtech repository
	Mods   []CatalogMod `json:"mods"`

	// Repository fields (docs: mods.frogtech.dev/docs/repositories).
	Format      int    `json:"format"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Icon        string `json:"icon"`
}

// LoaderInfo is the newest Frogtech Mod Loader build.
type LoaderInfo struct {
	Version string   `json:"version"`
	URL     string   `json:"url"`
	SHA256  string   `json:"sha256"`
	Notes   []string `json:"notes,omitempty"` // "what's new" lines, shown before updating
}

type CatalogMod struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Game        string   `json:"game"` // ClientVersion the build targets
	URL         string   `json:"url"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	Description string   `json:"description"`
	Page        string   `json:"page"`
	Conflicts   []string `json:"conflicts"`
	Image       string   `json:"image,omitempty"` // preview image (absolute URL)
	Tags        []string `json:"tags,omitempty"`  // what the mod is about: HUD, Video, Social...
	Notes       []string `json:"notes,omitempty"` // what changed in this version
}

// defaultCatalogTags covers catalogs published before the "tags" field.
var defaultCatalogTags = map[string][]string{
	"uikit":            {"HUD", "Editor", "Theme"},
	"aspect_4x3":       {"Video", "Settings"},
	"steam_profiles":   {"Social", "Menus"},
	"hero_select_plus": {"Menus", "New players"},
	"hud_fit":          {"HUD", "Retired"},
}

// TagsOf returns a catalog mod's tags, falling back to the built-in list.
func (m *CatalogMod) TagsOf() []string {
	if len(m.Tags) > 0 {
		return m.Tags
	}
	return defaultCatalogTags[m.ID]
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func userAgent() string { return "FrogLoader/" + version }

// HTTPStatusError is a non-200 reply.
type HTTPStatusError struct {
	URL  string
	Code int
}

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.URL, e.Code) }

func httpGet(ctx context.Context, url string) (*http.Response, error) {
	return httpGetAuth(ctx, url, nil)
}

// httpGetAuth is httpGet that adds a repository key where it may go (see
// repoAuth): on the request and on redirects, re-checked for every hop.
func httpGetAuth(ctx context.Context, url string, auth *repoAuth) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	auth.apply(req)
	client := *httpClient
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		r.Header.Del("Authorization")
		auth.apply(r)
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &HTTPStatusError{URL: url, Code: resp.StatusCode}
	}
	return resp, nil
}

// FetchCatalog downloads and parses the catalog.
func FetchCatalog(ctx context.Context, url string) (*Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var c Catalog
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	for _, m := range c.Mods {
		if m.ID == "" || !validID(m.ID) {
			return nil, fmt.Errorf("catalog: invalid mod id %q", m.ID)
		}
	}
	return &c, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return id != "." && id != ".."
}

func (c *Catalog) Mod(id string) *CatalogMod {
	if c == nil {
		return nil
	}
	for i := range c.Mods {
		if c.Mods[i].ID == id {
			return &c.Mods[i]
		}
	}
	return nil
}

// CompareVersions compares dotted versions numerically ("0.10.0" > "0.9.1").
// A pre-release suffix ("0.1.0-dev") sorts before the release.
func CompareVersions(a, b string) int {
	a, b = strings.TrimPrefix(strings.TrimSpace(a), "v"), strings.TrimPrefix(strings.TrimSpace(b), "v")
	aCore, aPre, _ := strings.Cut(a, "-")
	bCore, bPre, _ := strings.Cut(b, "-")
	ap, bp := strings.Split(aCore, "."), strings.Split(bCore, ".")
	for i := 0; i < len(ap) || i < len(bp); i++ {
		var x, y string
		if i < len(ap) {
			x = ap[i]
		}
		if i < len(bp) {
			y = bp[i]
		}
		xn, xerr := strconv.Atoi(orZero(x))
		yn, yerr := strconv.Atoi(orZero(y))
		if xerr == nil && yerr == nil {
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	switch {
	case aPre == bPre:
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	}
	return strings.Compare(aPre, bPre)
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// Checksum to verify a download against.
type Checksum struct {
	Algo string // "sha256" or "md5"
	Hex  string
	Size int64 // 0 = unknown
}

// Download fetches url into dest (created/truncated), verifying the checksum.
// On failure dest is removed.
func Download(ctx context.Context, url, dest string, sum Checksum) error {
	return DownloadAuth(ctx, url, dest, sum, nil)
}

// DownloadAuth is Download with a repository key (sent only to the
// repository's own origin).
func DownloadAuth(ctx context.Context, url, dest string, sum Checksum, auth *repoAuth) error {
	if sum.Hex == "" && sum.Algo != "none" {
		return errors.New("no checksum for " + url)
	}
	resp, err := httpGetAuth(ctx, url, auth)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	var h hash.Hash = sha256.New()
	if strings.EqualFold(sum.Algo, "md5") {
		h = md5.New()
	}
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, maxVPKSize))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && sum.Size > 0 && n != sum.Size {
		err = fmt.Errorf("download size %d, expected %d", n, sum.Size)
	}
	if err == nil && sum.Algo != "none" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum.Hex) {
			err = fmt.Errorf("checksum mismatch for %s (got %s, expected %s)", url, got, sum.Hex)
		}
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}
