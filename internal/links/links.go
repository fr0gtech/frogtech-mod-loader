// Package links parses frogmod:// links, which let websites hand work to the
// loader:
//
//	frogmod://install/<catalog id>          install or update a Frogtech mod and enable it
//	frogmod://gamebanana/<mod id>           open a GameBanana mod's details in Browse
//	frogmod://gamebanana/sound/<sound id>   same for a GameBanana sound
//	frogmod://install/<repoId>/<modId>      install from a repository the user added
//	frogmod://repo/add?url=<address>        open the Add repository dialog
//
// Windows starts `"<exe>" --url "<link>"`; a second instance forwards the link
// to the running one (POST /api/url) and exits.
package links

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/fr0gtech/frogtech-mod-loader/internal/catalog"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
)

// Scheme is the URL scheme the loader registers.
const Scheme = "frogmod"

// Link is a parsed frogmod:// link.
type Link struct {
	Action string // "install", "gamebanana" or "repo-add"
	Repo   string // install: repository id ("" = Frogtech)
	ID     string // install: mod id
	URL    string // repo-add: the repository address
	Model  string // gamebanana.ModelMod or gamebanana.ModelSound (gamebanana)
	GBID   int
}

// Parse validates a frogmod:// link.
func Parse(raw string) (Link, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 512 {
		return Link{}, errors.New("link too long")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, Scheme) {
		return Link{}, fmt.Errorf("not a %s:// link: %q", Scheme, raw)
	}
	// frogmod://install/uikit parses as host "install", path "/uikit".
	// Also accept frogmod:install/uikit and trailing slashes.
	parts := []string{}
	for _, p := range strings.Split(strings.Trim(u.Host+"/"+strings.TrimPrefix(u.Opaque, "//")+u.Path, "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return Link{}, errors.New("empty frogmod:// link")
	}
	switch strings.ToLower(parts[0]) {
	case "install":
		switch {
		case len(parts) == 2 && catalog.ValidID(parts[1]):
			return Link{Action: "install", ID: parts[1]}, nil
		case len(parts) == 3 && catalog.ValidRepoID(parts[1]) && catalog.ValidRepoModID(parts[2]):
			return Link{Action: "install", Repo: parts[1], ID: parts[2]}, nil
		}
		return Link{}, fmt.Errorf("bad install link %q", raw)
	case "repo":
		if len(parts) != 2 || !strings.EqualFold(parts[1], "add") {
			return Link{}, fmt.Errorf("bad repository link %q", raw)
		}
		ru, err := catalog.CheckURL(u.Query().Get("url"))
		if err != nil {
			return Link{}, fmt.Errorf("repository link: address %v", err)
		}
		return Link{Action: "repo-add", URL: ru.String()}, nil
	case "gamebanana", "gb":
		model := gamebanana.ModelMod
		rest := parts[1:]
		if len(rest) == 2 {
			switch strings.ToLower(rest[0]) {
			case "sound", "sounds":
				model = gamebanana.ModelSound
			case "mod", "mods":
			default:
				return Link{}, fmt.Errorf("bad GameBanana link %q", raw)
			}
			rest = rest[1:]
		}
		if len(rest) != 1 {
			return Link{}, fmt.Errorf("bad GameBanana link %q", raw)
		}
		id, err := strconv.Atoi(rest[0])
		if err != nil || id <= 0 {
			return Link{}, fmt.Errorf("bad GameBanana id in %q", raw)
		}
		return Link{Action: "gamebanana", Model: model, GBID: id}, nil
	}
	return Link{}, fmt.Errorf("unknown frogmod:// action %q", parts[0])
}
