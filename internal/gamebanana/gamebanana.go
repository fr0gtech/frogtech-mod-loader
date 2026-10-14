// Package gamebanana talks to GameBanana's public API v11 for Deadlock (game
// 20948): items and their files for installs and updates, plus browsing.
package gamebanana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
)

const (
	gbBase = "https://gamebanana.com/apiv11"
	GameID = 20948 // https://gamebanana.com/games/20948
)

// GameBanana item types ("models") we install. Mods and Sounds are separate
// sections on the site, with their own ids, categories and endpoints.
const (
	ModelMod   = "Mod"
	ModelSound = "Sound"
)

func NormModel(m string) string {
	if strings.EqualFold(m, ModelSound) || strings.EqualFold(m, "sounds") {
		return ModelSound
	}
	return ModelMod
}

// File is one downloadable file of a GameBanana submission (API v11 names).
type File struct {
	ID          int    `json:"_idRow"`
	Name        string `json:"_sFile"`
	Size        int64  `json:"_nFilesize"`
	DateAdded   int64  `json:"_tsDateAdded"`
	URL         string `json:"_sDownloadUrl"`
	MD5         string `json:"_sMd5Checksum"`
	IsArchived  bool   `json:"_bIsArchived"`
	AvResult    string `json:"_sAvResult"`
	Description string `json:"_sDescription,omitempty"`
	Downloads   int    `json:"_nDownloadCount"`
}

// Mod is the subset of /apiv11/<Model>/<id> we use for installs and updates.
type Mod struct {
	ID           int       `json:"-"`
	Model        string    `json:"-"`
	Name         string    `json:"_sName"`
	Version      string    `json:"_sVersion"`
	DateModified int64     `json:"_tsDateModified"`
	DateUpdated  int64     `json:"_tsDateUpdated"`
	ProfileURL   string    `json:"_sProfileUrl"`
	Files        []File    `json:"_aFiles"`
	Preview      gbPreview `json:"_aPreviewMedia"`
	Category     gbNamed   `json:"_aCategory"`
	RootCategory gbNamed   `json:"_aRootCategory"`
	Game         struct {
		ID   int    `json:"_idRow"`
		Name string `json:"_sName"`
	} `json:"_aGame"`
	Submitter struct {
		Name string `json:"_sName"`
	} `json:"_aSubmitter"`
}

var gbURLRe = regexp.MustCompile(`(?i)gamebanana\.com/(mods|sounds)/(?:download/)?(\d+)`)

// ParseURL accepts https://gamebanana.com/mods/<id>,
// https://gamebanana.com/sounds/<id> or a bare mod id.
func ParseURL(s string) (model string, id int, err error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return ModelMod, n, nil
	}
	m := gbURLRe.FindStringSubmatch(s)
	if m == nil {
		return "", 0, errors.New("expected a GameBanana link like https://gamebanana.com/mods/123456")
	}
	id, err = strconv.Atoi(m[2])
	return NormModel(m[1]), id, err
}

func parseMod(data []byte, model string, id int) (*Mod, error) {
	if err := gbError(data); err != nil {
		return nil, err
	}
	var m Mod
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("GameBanana: %w", err)
	}
	m.ID, m.Model = id, NormModel(model)
	if m.Name == "" {
		return nil, errors.New("GameBanana: not found")
	}
	if m.Game.ID != 0 && m.Game.ID != GameID {
		return nil, fmt.Errorf("GameBanana item %d is for %s, not Deadlock", id, m.Game.Name)
	}
	if m.ProfileURL == "" {
		m.ProfileURL = gbPage(m.Model, id)
	}
	return &m, nil
}

func gbPage(model string, id int) string {
	return fmt.Sprintf("https://gamebanana.com/%ss/%d", strings.ToLower(NormModel(model)), id)
}

// gbError turns {"_sErrorCode": ...} replies into errors.
func gbError(data []byte) error {
	var e struct {
		Code string `json:"_sErrorCode"`
		Msg  string `json:"_sErrorMessage"`
	}
	if json.Unmarshal(data, &e) == nil && e.Code != "" {
		if e.Msg == "" {
			e.Msg = e.Code
		}
		return errors.New("GameBanana: " + e.Msg)
	}
	return nil
}

// gbGet fetches a GameBanana API path; ttl > 0 caches the reply.
func gbGet(ctx context.Context, path string, ttl time.Duration) ([]byte, error) {
	u := gbBase + path
	if ttl > 0 {
		if b, ok := gbCache.get(u); ok {
			return b, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if err := gbError(data); err != nil {
		return nil, err
	}
	if ttl > 0 {
		gbCache.put(u, data, ttl)
	}
	return data, nil
}

// FetchMod queries the GameBanana API for an item (not cached: used for
// update checks and installs).
func FetchMod(ctx context.Context, model string, id int) (*Mod, error) {
	model = NormModel(model)
	props := "_sName,_aFiles,_tsDateModified,_tsDateUpdated,_aGame,_aSubmitter,_sProfileUrl,_aPreviewMedia,_aCategory"
	if model == ModelMod {
		props += ",_sVersion,_aRootCategory" // Sounds have neither field
	}
	data, err := gbGet(ctx, fmt.Sprintf("/%s/%d?_csvProperties=%s", model, id, url.QueryEscape(props)), 0)
	if err != nil {
		return nil, err
	}
	return parseMod(data, model, id)
}

// CategoryName is the top-level category (e.g. "Skins"), or the category for Sounds.
func (m *Mod) CategoryName() string {
	if m.RootCategory.Name != "" {
		return m.RootCategory.Name
	}
	return m.Category.Name
}

// Thumb returns the first preview image (530px if available), or "".
func (m *Mod) Thumb() string {
	for _, im := range m.Preview.Images {
		if u := im.url(im.File530, im.File220, im.File); u != "" {
			return u
		}
	}
	return ""
}

// ActiveFiles returns non-archived files, newest first.
func (m *Mod) ActiveFiles() []File { return activeFiles(m.Files) }

func activeFiles(files []File) []File {
	var out []File
	for _, f := range files {
		if !f.IsArchived {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		out = append(out, files...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DateAdded > out[j].DateAdded })
	return out
}

// Newest returns the most recently added non-archived file.
func (m *Mod) Newest() (File, bool) {
	f := m.ActiveFiles()
	if len(f) == 0 {
		return File{}, false
	}
	return f[0], true
}

// File returns the file with the given id.
func (m *Mod) File(id int) (File, bool) {
	for _, f := range m.Files {
		if f.ID == id {
			return f, true
		}
	}
	return File{}, false
}

// ModID is the local mod id; plain "gb-<id>" for Mods (as in 0.1.0).
func ModID(model string, id int) string {
	if NormModel(model) == ModelSound {
		return "gb-sound-" + strconv.Itoa(id)
	}
	return "gb-" + strconv.Itoa(id)
}

func Key(model string, id int) string { return NormModel(model) + ":" + strconv.Itoa(id) }

// UpdateAvailable decides whether an installed item (its file id, and whether
// the user pinned an older file) should update to remote's newest file.
func UpdateAvailable(fileID int, pinned bool, remote *Mod) (File, bool) {
	if pinned {
		return File{}, false
	}
	f, ok := remote.Newest()
	if !ok || f.ID == fileID {
		return File{}, false
	}
	return f, true
}
