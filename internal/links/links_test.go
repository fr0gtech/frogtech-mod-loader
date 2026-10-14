package links

import (
	"strings"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/gamebanana"
)

func TestParseFrogURL(t *testing.T) {
	ok := map[string]Link{
		"frogmod://install/uikit":                                             {Action: "install", ID: "uikit"},
		"frogmod://install/hero_select_plus/":                                 {Action: "install", ID: "hero_select_plus"},
		"FROGMOD://Install/aspect_4x3":                                        {Action: "install", ID: "aspect_4x3"},
		"frogmod:install/steam_profiles":                                      {Action: "install", ID: "steam_profiles"},
		"frogmod://gamebanana/650634":                                         {Action: "gamebanana", Model: gamebanana.ModelMod, GBID: 650634},
		"frogmod://gamebanana/mod/650634":                                     {Action: "gamebanana", Model: gamebanana.ModelMod, GBID: 650634},
		"frogmod://gamebanana/sound/83198":                                    {Action: "gamebanana", Model: gamebanana.ModelSound, GBID: 83198},
		" frogmod://gb/sounds/83198 ":                                         {Action: "gamebanana", Model: gamebanana.ModelSound, GBID: 83198},
		"frogmod://install/example.com-1a2b3c4d/cool_hud":                     {Action: "install", Repo: "example.com-1a2b3c4d", ID: "cool_hud"},
		"frogmod://repo/add?url=https%3A%2F%2Fexample.com%2Fmods%2Frepo.json": {Action: "repo-add", URL: "https://example.com/mods/repo.json"},
		"frogmod://repo/add?url=http%3A%2F%2Flocalhost%3A8080%2Fr.json":       {Action: "repo-add", URL: "http://localhost:8080/r.json"},
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	bad := []string{"", "frogmod://", "frogmod://install", "frogmod://install/../evil", "frogmod://install/a/b/c", "frogmod://install/../x",
		"frogmod://install/Repo.example/Mod", "https://install/uikit", "frogmod://gamebanana/abc", "frogmod://gamebanana/-1",
		"frogmod://gamebanana/skin/5", "frogmod://delete/everything", "frogmod://install/" + strings.Repeat("x", 600),
		"\x00frogmod://install/x", "frogmod://repo/add", "frogmod://repo/add?url=http%3A%2F%2Fexample.com%2Fr.json",
		"frogmod://repo/add?url=ftp%3A%2F%2Fexample.com%2Fr.json", "frogmod://repo/remove?url=https%3A%2F%2Fexample.com"}
	for _, in := range bad {
		if got, err := Parse(in); err == nil {
			t.Errorf("%q accepted: %+v", in, got)
		}
	}
}
