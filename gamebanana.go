package main

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
)

const (
	gbBase         = "https://gamebanana.com/apiv11"
	gbDeadlockGame = 20948 // https://gamebanana.com/games/20948
)

// GameBanana item types ("models") we install. Mods and Sounds are separate
// sections on the site, with their own ids, categories and endpoints.
const (
	ModelMod   = "Mod"
	ModelSound = "Sound"
)

func normModel(m string) string {
	if strings.EqualFold(m, ModelSound) || strings.EqualFold(m, "sounds") {
		return ModelSound
	}
	return ModelMod
}

// GBFile is one downloadable file of a GameBanana submission (API v11 names).
type GBFile struct {
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

// GBMod is the subset of /apiv11/<Model>/<id> we use for installs and updates.
type GBMod struct {
	ID           int       `json:"-"`
	Model        string    `json:"-"`
	Name         string    `json:"_sName"`
	Version      string    `json:"_sVersion"`
	DateModified int64     `json:"_tsDateModified"`
	DateUpdated  int64     `json:"_tsDateUpdated"`
	ProfileURL   string    `json:"_sProfileUrl"`
	Files        []GBFile  `json:"_aFiles"`
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

// ParseGameBananaURL accepts https://gamebanana.com/mods/<id>,
// https://gamebanana.com/sounds/<id> or a bare mod id.
func ParseGameBananaURL(s string) (model string, id int, err error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return ModelMod, n, nil
	}
	m := gbURLRe.FindStringSubmatch(s)
	if m == nil {
		return "", 0, errors.New("expected a GameBanana link like https://gamebanana.com/mods/123456")
	}
	id, err = strconv.Atoi(m[2])
	return normModel(m[1]), id, err
}

func parseGBMod(data []byte, model string, id int) (*GBMod, error) {
	if err := gbError(data); err != nil {
		return nil, err
	}
	var m GBMod
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("GameBanana: %w", err)
	}
	m.ID, m.Model = id, normModel(model)
	if m.Name == "" {
		return nil, errors.New("GameBanana: not found")
	}
	if m.Game.ID != 0 && m.Game.ID != gbDeadlockGame {
		return nil, fmt.Errorf("GameBanana item %d is for %s, not Deadlock", id, m.Game.Name)
	}
	if m.ProfileURL == "" {
		m.ProfileURL = gbPage(m.Model, id)
	}
	return &m, nil
}

func gbPage(model string, id int) string {
	return fmt.Sprintf("https://gamebanana.com/%ss/%d", strings.ToLower(normModel(model)), id)
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
	resp, err := httpGet(ctx, u)
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

// FetchGBMod queries the GameBanana API for an item (not cached: used for
// update checks and installs).
func FetchGBMod(ctx context.Context, model string, id int) (*GBMod, error) {
	model = normModel(model)
	props := "_sName,_aFiles,_tsDateModified,_tsDateUpdated,_aGame,_aSubmitter,_sProfileUrl,_aPreviewMedia,_aCategory"
	if model == ModelMod {
		props += ",_sVersion,_aRootCategory" // Sounds have neither field
	}
	data, err := gbGet(ctx, fmt.Sprintf("/%s/%d?_csvProperties=%s", model, id, url.QueryEscape(props)), 0)
	if err != nil {
		return nil, err
	}
	return parseGBMod(data, model, id)
}

// CategoryName is the top-level category (e.g. "Skins"), or the category for Sounds.
func (m *GBMod) CategoryName() string {
	if m.RootCategory.Name != "" {
		return m.RootCategory.Name
	}
	return m.Category.Name
}

// Thumb returns the first preview image (530px if available), or "".
func (m *GBMod) Thumb() string {
	for _, im := range m.Preview.Images {
		if u := im.url(im.File530, im.File220, im.File); u != "" {
			return u
		}
	}
	return ""
}

// ActiveFiles returns non-archived files, newest first.
func (m *GBMod) ActiveFiles() []GBFile { return activeFiles(m.Files) }

func activeFiles(files []GBFile) []GBFile {
	var out []GBFile
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
func (m *GBMod) Newest() (GBFile, bool) {
	f := m.ActiveFiles()
	if len(f) == 0 {
		return GBFile{}, false
	}
	return f[0], true
}

// File returns the file with the given id.
func (m *GBMod) File(id int) (GBFile, bool) {
	for _, f := range m.Files {
		if f.ID == id {
			return f, true
		}
	}
	return GBFile{}, false
}

// gbModID is the local mod id; plain "gb-<id>" for Mods (as in 0.1.0).
func gbModID(model string, id int) string {
	if normModel(model) == ModelSound {
		return "gb-sound-" + strconv.Itoa(id)
	}
	return "gb-" + strconv.Itoa(id)
}

func gbKey(model string, id int) string { return normModel(model) + ":" + strconv.Itoa(id) }

// GBUpdateAvailable decides whether an installed GameBanana mod should update.
func GBUpdateAvailable(installed *Mod, remote *GBMod) (GBFile, bool) {
	if installed.GBPinned {
		return GBFile{}, false
	}
	f, ok := remote.Newest()
	if !ok || f.ID == installed.GBFileID {
		return GBFile{}, false
	}
	return f, true
}
