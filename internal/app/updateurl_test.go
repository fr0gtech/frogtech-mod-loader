package app

import "testing"

func TestUpdateURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://mods.frogtech.dev/dl/uikit_v0.8.2.zip":      "https://mods.frogtech.dev/dl/uikit_v0.8.2.zip?update=1",
		"https://mods.frogtech.dev/downloads/x.zip":          "https://mods.frogtech.dev/downloads/x.zip",
		"https://example.com/dl/mod.zip":                     "https://example.com/dl/mod.zip",
		"https://mods.frogtech.dev/dl/a.zip?update=1":        "https://mods.frogtech.dev/dl/a.zip?update=1",
		"https://mods.frogtech.dev/dl/FrogLoader_v0.6.1.zip": "https://mods.frogtech.dev/dl/FrogLoader_v0.6.1.zip?update=1",
	} {
		if got := updateURL(in); got != want {
			t.Errorf("updateURL(%q) = %q, want %q", in, got, want)
		}
	}
}
