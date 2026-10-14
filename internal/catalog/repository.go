package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
)

// Repository status, as shown in Settings > Repositories.
const (
	StatusOK          = "ok"
	StatusKeyRejected = "key"         // 401/403: "Key not accepted"
	StatusUnreachable = "unreachable" // network error or other HTTP status
	StatusInvalid     = "invalid"     // not a valid repository file
)

var (
	repoModIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	repoIDRe    = regexp.MustCompile(`^[a-z0-9._-]{1,120}$`)
	sha256Re    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// CheckURL accepts https URLs, and http only for localhost/127.0.0.1.
func CheckURL(raw string) (*url.URL, error) {
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

// RepoID derives a stable repository id: host (with port) plus a hash of
// the path and query, e.g. "mods.example.com-1a2b3c4d".
func RepoID(u *url.URL) string {
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

// ValidModID accepts our plain ids and "<repoId>/<modId>".
func ValidModID(id string) bool {
	repo, mod, ok := strings.Cut(id, "/")
	if !ok {
		return ValidID(id)
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
		if _, err := CheckURL(m.URL); err != nil {
			return nil, fmt.Errorf("%s: url %v", where, err)
		}
		seen[m.ID] = true
	}
	c.Loader = LoaderInfo{} // only the built-in repository may offer loader updates
	return &c, nil
}

// FetchRepo downloads and validates a repository. status is one of the repo*
// constants; err explains anything but StatusOK.
func FetchRepo(ctx context.Context, u *url.URL, key string) (*Catalog, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := fetch.GetAuth(ctx, u.String(), &fetch.Auth{Origin: u, Key: key})
	if err != nil {
		var hs *fetch.StatusError
		if errors.As(err, &hs) && (hs.Code == http.StatusUnauthorized || hs.Code == http.StatusForbidden) {
			return nil, StatusKeyRejected, errors.New("key not accepted")
		}
		if errors.As(err, &hs) {
			return nil, StatusUnreachable, fmt.Errorf("the server answered HTTP %d", hs.Code)
		}
		return nil, StatusUnreachable, fmt.Errorf("can't reach it: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil {
		return nil, StatusUnreachable, err
	}
	if len(data) > 4<<20 {
		return nil, StatusInvalid, errors.New("the file is larger than 4 MB")
	}
	c, err := ParseRepo(data)
	if err != nil {
		return nil, StatusInvalid, err
	}
	return c, StatusOK, nil
}

// ---- App ----

// ValidRepoID reports whether id is a repository id (see RepoID).
func ValidRepoID(id string) bool { return repoIDRe.MatchString(id) && !strings.Contains(id, "..") }

// ValidRepoModID reports whether id is a valid mod id inside a repository.
func ValidRepoModID(id string) bool { return repoModIDRe.MatchString(id) }
