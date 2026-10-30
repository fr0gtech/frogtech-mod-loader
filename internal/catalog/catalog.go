// Package catalog reads mod repositories: the built-in Frogtech catalog and
// third-party repositories (format: mods.frogtech.dev/docs/repositories).
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
)

var FrogtechURL = "https://mods.frogtech.dev/loader/catalog.json"

// Catalog is the list of our own mods plus the latest loader build.
type Catalog struct {
	Loader LoaderInfo `json:"loader"` // honoured only from the built-in Frogtech repository
	Mods   []Mod      `json:"mods"`

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

type Mod struct {
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
	// Builds is the URL of a builds.json listing this mod's files per game
	// build (ClientVersion), for mods that break when Steam updates the game.
	Builds string `json:"builds,omitempty"`
}

// defaultTags covers catalogs published before the "tags" field.
var defaultTags = map[string][]string{
	"uikit":            {"HUD", "Editor", "Theme"},
	"aspect_4x3":       {"Video", "Settings"},
	"steam_profiles":   {"Social", "Menus"},
	"hero_select_plus": {"Menus", "New players"},
	"hud_fit":          {"HUD", "Retired"},
}

// TagsOf returns a catalog mod's tags, falling back to the built-in list.
func (m *Mod) TagsOf() []string {
	if len(m.Tags) > 0 {
		return m.Tags
	}
	return defaultTags[m.ID]
}

// Fetch downloads and parses the catalog.
func Fetch(ctx context.Context, url string) (*Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var c Catalog
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&c); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	for _, m := range c.Mods {
		if m.ID == "" || !ValidID(m.ID) {
			return nil, fmt.Errorf("catalog: invalid mod id %q", m.ID)
		}
	}
	return &c, nil
}

func ValidID(id string) bool {
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

func (c *Catalog) Mod(id string) *Mod {
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

// Build is one entry of a builds.json: the mod built for one game build.
type Build struct {
	Version   string `json:"version"`
	File      string `json:"file"` // zip name, next to builds.json
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Published bool   `json:"published"`
	URL       string `json:"-"` // the zip: builds.json's folder + File
}

// FetchBuilds downloads a builds.json ({"format": 1, "builds": {"<ClientVersion>":
// Build}}) and returns its published builds by ClientVersion, each with URL set.
// Entries not published, or without a plain file name or checksum, are left out.
func FetchBuilds(ctx context.Context, buildsURL string) (map[string]Build, error) {
	base, err := url.Parse(buildsURL)
	if err != nil {
		return nil, fmt.Errorf("builds: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch.Get(ctx, buildsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Format int              `json:"format"`
		Builds map[string]Build `json:"builds"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("builds: %w", err)
	}
	if doc.Format != 1 {
		return nil, fmt.Errorf("builds: unsupported format %d", doc.Format)
	}
	out := map[string]Build{}
	for ver, b := range doc.Builds {
		if !b.Published || b.SHA256 == "" || b.File == "" || b.File == "." || b.File == ".." ||
			strings.ContainsAny(b.File, "/\\?#%:") {
			continue
		}
		b.URL = base.ResolveReference(&url.URL{Path: b.File}).String()
		out[ver] = b
	}
	return out, nil
}
