package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Browsing GameBanana's Deadlock section through its public API v11:
//
//	list:       /<Model>/Index?_aFilters[Generic_Game]=20948&_aFilters[Generic_Category]=<cat>&_sSort=<sort>&_nPage=<n>&_nPerpage=24
//	search:     /Util/Search/Results?_sModelName=<Model>&_idGameRow=20948&_sSearchString=<q>&_sOrder=<order>&_nPage=<n>
//	categories: /<Model>/Categories?_idGameRow=20948&_sSort=a_to_z&_bShowEmpty=true  (children: _idCategoryRow=<id>)
//	details:    /<Model>/<id>/ProfilePage
//
// Replies are cached in memory (lists/details 5 min, categories 1 h).

const (
	browsePerPage = 24
	listTTL       = 5 * time.Minute
	catTTL        = time.Hour
)

// ---- response cache ----

type cacheEntry struct {
	data    []byte
	expires time.Time
}

type respCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

var gbCache = &respCache{m: map[string]cacheEntry{}}

func (c *respCache) get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (c *respCache) put(k string, data []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= 300 {
		now := time.Now()
		for key, e := range c.m {
			if now.After(e.expires) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= 300 {
			c.m = map[string]cacheEntry{}
		}
	}
	c.m[k] = cacheEntry{data, time.Now().Add(ttl)}
}

// ---- API shapes ----

type gbImage struct {
	Base    string `json:"_sBaseUrl"`
	File    string `json:"_sFile"`
	File220 string `json:"_sFile220"`
	File530 string `json:"_sFile530"`
	File800 string `json:"_sFile800"`
}

func (i gbImage) url(prefer ...string) string {
	if i.Base == "" {
		return ""
	}
	for _, p := range prefer {
		if p != "" {
			return i.Base + "/" + p
		}
	}
	return i.Base + "/" + i.File
}

type gbPreview struct {
	Images   []gbImage `json:"_aImages"`
	Metadata struct {
		AudioURL string `json:"_sAudioUrl"`
	} `json:"_aMetadata"`
}

// UnmarshalJSON tolerates GameBanana's empty-array-for-empty-object quirk.
func (p *gbPreview) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		return nil
	}
	type plain gbPreview
	return json.Unmarshal(b, (*plain)(p))
}

type gbNamed struct {
	ID   int    `json:"_idRow"`
	Name string `json:"_sName"`
}

func (n *gbNamed) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '[' {
		return nil
	}
	type plain gbNamed
	return json.Unmarshal(b, (*plain)(n))
}

type gbRecord struct {
	ID           int       `json:"_idRow"`
	Model        string    `json:"_sModelName"`
	Name         string    `json:"_sName"`
	ProfileURL   string    `json:"_sProfileUrl"`
	DateAdded    int64     `json:"_tsDateAdded"`
	DateModified int64     `json:"_tsDateModified"`
	DateUpdated  int64     `json:"_tsDateUpdated"`
	HasFiles     bool      `json:"_bHasFiles"`
	Preview      gbPreview `json:"_aPreviewMedia"`
	Submitter    gbNamed   `json:"_aSubmitter"`
	Game         gbNamed   `json:"_aGame"`
	RootCategory gbNamed   `json:"_aRootCategory"`
	SubCategory  gbNamed   `json:"_aSubCategory"`
	Version      string    `json:"_sVersion"`
	Visibility   string    `json:"_sInitialVisibility"`
	HasRatings   bool      `json:"_bHasContentRatings"`
	Likes        int       `json:"_nLikeCount"`
	Views        int       `json:"_nViewCount"`
	Posts        int       `json:"_nPostCount"`
	Obsolete     bool      `json:"_bIsObsolete"`
}

type gbList struct {
	Metadata struct {
		Count    int  `json:"_nRecordCount"`
		Complete bool `json:"_bIsComplete"`
		PerPage  int  `json:"_nPerpage"`
	} `json:"_aMetadata"`
	Records []gbRecord `json:"_aRecords"`
}

// ---- normalized shapes for the page ----

// BrowseItem is one card in the Browse tab.
type BrowseItem struct {
	ID        int    `json:"id"`
	Model     string `json:"model"`
	Name      string `json:"name"`
	Author    string `json:"author"`
	Thumb     string `json:"thumb"`
	Category  string `json:"category"`
	Likes     int    `json:"likes"`
	Views     int    `json:"views"`
	Added     int64  `json:"added"`
	Updated   int64  `json:"updated"`
	Version   string `json:"version,omitempty"`
	NSFW      bool   `json:"nsfw"`
	Page      string `json:"page"`
	Installed bool   `json:"installed"`
}

// BrowseResult is a page of results.
type BrowseResult struct {
	Items   []BrowseItem `json:"items"`
	Page    int          `json:"page"`
	Total   int          `json:"total"`
	PerPage int          `json:"perPage"`
	More    bool         `json:"more"`
}

// BrowseQuery selects a page of results.
type BrowseQuery struct {
	Model    string
	Sort     string // liked, downloaded, viewed, newest, updated (or "" = relevance when searching)
	Category int
	Search   string
	Page     int
}

var indexSorts = map[string]string{
	"liked": "Generic_MostLiked", "downloaded": "Generic_MostDownloaded", "viewed": "Generic_MostViewed",
	"newest": "Generic_Newest", "updated": "Generic_LatestUpdated", "commented": "Generic_MostCommented",
}

var searchOrders = map[string]string{
	"": "best_match", "relevance": "best_match", "liked": "popularity", "downloaded": "popularity",
	"viewed": "popularity", "newest": "date", "updated": "udate",
}

func browsePath(q BrowseQuery) string {
	model := normModel(q.Model)
	page := q.Page
	if page < 1 {
		page = 1
	}
	v := url.Values{}
	if s := strings.TrimSpace(q.Search); s != "" {
		order, ok := searchOrders[q.Sort]
		if !ok {
			order = "best_match"
		}
		v.Set("_sModelName", model)
		v.Set("_idGameRow", fmt.Sprint(gbDeadlockGame))
		v.Set("_sSearchString", s)
		v.Set("_sOrder", order)
		v.Set("_nPage", fmt.Sprint(page))
		return "/Util/Search/Results?" + v.Encode()
	}
	sortKey, ok := indexSorts[q.Sort]
	if !ok {
		sortKey = "Generic_MostLiked"
	}
	v.Set("_aFilters[Generic_Game]", fmt.Sprint(gbDeadlockGame))
	if q.Category > 0 {
		v.Set("_aFilters[Generic_Category]", fmt.Sprint(q.Category))
	}
	v.Set("_sSort", sortKey)
	v.Set("_nPage", fmt.Sprint(page))
	v.Set("_nPerpage", fmt.Sprint(browsePerPage))
	return fmt.Sprintf("/%s/Index?%s", model, v.Encode())
}

// isNSFW mirrors GameBanana: items it hides or warns about by default.
func isNSFW(visibility string) bool {
	return visibility == "hide" || visibility == "warn"
}

func normalizeList(data []byte, model string, page int) (*BrowseResult, error) {
	var l gbList
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("GameBanana: %w", err)
	}
	res := &BrowseResult{Page: page, Total: l.Metadata.Count, PerPage: l.Metadata.PerPage, Items: []BrowseItem{}}
	for _, r := range l.Records {
		if r.Model != "" && r.Model != normModel(model) {
			continue
		}
		if r.Game.ID != 0 && r.Game.ID != gbDeadlockGame {
			continue
		}
		it := BrowseItem{ID: r.ID, Model: normModel(model), Name: r.Name, Author: r.Submitter.Name, Likes: r.Likes,
			Views: r.Views, Added: r.DateAdded, Updated: max(r.DateUpdated, r.DateAdded), Version: r.Version,
			NSFW: isNSFW(r.Visibility), Page: r.ProfileURL}
		if it.Page == "" {
			it.Page = gbPage(model, r.ID)
		}
		it.Category = r.RootCategory.Name
		if r.SubCategory.Name != "" {
			it.Category += " / " + r.SubCategory.Name
		}
		if len(r.Preview.Images) > 0 {
			im := r.Preview.Images[0]
			it.Thumb = im.url(im.File220, im.File530, im.File)
		}
		res.Items = append(res.Items, it)
	}
	res.More = !l.Metadata.Complete && len(l.Records) > 0 && page*max(res.PerPage, 1) < res.Total
	return res, nil
}

// Browse returns one page of GameBanana results.
func Browse(ctx context.Context, q BrowseQuery) (*BrowseResult, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	data, err := gbGet(ctx, browsePath(q), listTTL)
	if err != nil {
		return nil, err
	}
	return normalizeList(data, q.Model, q.Page)
}

// ---- categories ----

// Category is a GameBanana category with optional children.
type Category struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	Count    int        `json:"count"`
	Children []Category `json:"children,omitempty"`
}

type gbCategory struct {
	ID         int    `json:"_idRow"`
	Name       string `json:"_sName"`
	Items      int    `json:"_nItemCount"`
	Categories int    `json:"_nCategoryCount"`
}

func fetchCategories(ctx context.Context, model string, parent int) ([]gbCategory, error) {
	v := url.Values{}
	if parent > 0 {
		v.Set("_idCategoryRow", fmt.Sprint(parent))
	} else {
		v.Set("_idGameRow", fmt.Sprint(gbDeadlockGame))
	}
	v.Set("_sSort", "a_to_z")
	v.Set("_bShowEmpty", "true")
	data, err := gbGet(ctx, fmt.Sprintf("/%s/Categories?%s", normModel(model), v.Encode()), catTTL)
	if err != nil {
		return nil, err
	}
	var cats []gbCategory
	if err := json.Unmarshal(data, &cats); err != nil {
		return nil, fmt.Errorf("GameBanana categories: %w", err)
	}
	return cats, nil
}

// Categories returns the category tree (two levels) for Mods or Sounds.
func Categories(ctx context.Context, model string) ([]Category, error) {
	roots, err := fetchCategories(ctx, model, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Category, len(roots))
	var wg sync.WaitGroup
	for i, r := range roots {
		out[i] = Category{ID: r.ID, Name: r.Name, Count: r.Items}
		if r.Categories == 0 {
			continue
		}
		wg.Add(1)
		go func(i, id int) {
			defer wg.Done()
			kids, err := fetchCategories(ctx, model, id)
			if err != nil {
				return
			}
			for _, k := range kids {
				out[i].Children = append(out[i].Children, Category{ID: k.ID, Name: k.Name, Count: k.Items})
			}
		}(i, r.ID)
	}
	wg.Wait()
	return out, nil
}

// ---- details ----

// ModDetails is the details view of one GameBanana item.
type ModDetails struct {
	ID          int      `json:"id"`
	Model       string   `json:"model"`
	Name        string   `json:"name"`
	Author      string   `json:"author"`
	Version     string   `json:"version,omitempty"`
	Category    string   `json:"category"`
	Summary     string   `json:"summary,omitempty"`
	Text        string   `json:"text"`
	Images      []string `json:"images"`     // gallery size (800px)
	Full        []string `json:"imagesFull"` // original size, for the lightbox
	Audio       string   `json:"audio,omitempty"`
	Files       []GBFile `json:"files"`
	Downloads   int      `json:"downloads"`
	Likes       int      `json:"likes"`
	Views       int      `json:"views"`
	Added       int64    `json:"added"`
	Updated     int64    `json:"updated"`
	NSFW        bool     `json:"nsfw"`
	Ratings     []string `json:"ratings,omitempty"`
	Page        string   `json:"page"`
	Installed   bool     `json:"installed"`
	InstalledID int      `json:"installedFileId,omitempty"`
}

type gbProfile struct {
	gbRecord
	Description string          `json:"_sDescription"`
	Text        string          `json:"_sText"`
	Downloads   int             `json:"_nDownloadCount"`
	Files       []GBFile        `json:"_aFiles"`
	RawRatings  json.RawMessage `json:"_aContentRatings"`
	Category    gbNamed         `json:"_aCategory"`
}

// Details fetches the profile page of a GameBanana item.
func Details(ctx context.Context, model string, id int) (*ModDetails, error) {
	model = normModel(model)
	data, err := gbGet(ctx, fmt.Sprintf("/%s/%d/ProfilePage", model, id), listTTL)
	if err != nil {
		return nil, err
	}
	return normalizeDetails(data, model, id)
}

func normalizeDetails(data []byte, model string, id int) (*ModDetails, error) {
	var p gbProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("GameBanana: %w", err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("GameBanana: %s %d not found", model, id)
	}
	if p.Game.ID != 0 && p.Game.ID != gbDeadlockGame {
		return nil, fmt.Errorf("this GameBanana item is for %s, not Deadlock", p.Game.Name)
	}
	d := &ModDetails{ID: id, Model: model, Name: p.Name, Author: p.Submitter.Name, Version: p.Version,
		Category: p.Category.Name, Summary: strings.TrimSpace(p.Description), Text: htmlToText(p.Text),
		Files: activeFiles(p.Files), Downloads: p.Downloads, Likes: p.Likes, Views: p.Views, Added: p.DateAdded,
		Updated: max(p.DateUpdated, p.DateModified, p.DateAdded), Page: p.ProfileURL, Images: []string{}, Full: []string{},
		Audio: p.Preview.Metadata.AudioURL}
	if d.Page == "" {
		d.Page = gbPage(model, id)
	}
	if d.Files == nil {
		d.Files = []GBFile{}
	}
	var ratings map[string]string
	if json.Unmarshal(p.RawRatings, &ratings) == nil {
		for _, v := range ratings {
			d.Ratings = append(d.Ratings, v)
		}
	}
	d.NSFW = isNSFW(p.Visibility)
	for _, im := range p.Preview.Images {
		if u := im.url(im.File800, im.File530, im.File); u != "" {
			d.Images = append(d.Images, u)
			d.Full = append(d.Full, im.url(im.File))
		}
	}
	return d, nil
}

var (
	reBlock    = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|h[1-6]|tr|hr|ul|ol|blockquote)\b[^>]*>`)
	reListItem = regexp.MustCompile(`(?i)<\s*li[^>]*>`)
	reTag      = regexp.MustCompile(`<[^>]*>`)
	reSpaces   = regexp.MustCompile(`[ \t\x{a0}]+`)
	reBlank    = regexp.MustCompile(`\n{3,}`)
	reBullet   = regexp.MustCompile(`\n+• `)
)

// htmlToText turns a GameBanana description into plain text. The page shows
// it as text, so no third-party HTML ever runs in the loader window.
func htmlToText(s string) string {
	s = reBlock.ReplaceAllString(s, "\n")
	s = reListItem.ReplaceAllString(s, "\n• ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	s = reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	s = reBullet.ReplaceAllString(s, "\n• ")
	return strings.TrimSpace(s)
}
