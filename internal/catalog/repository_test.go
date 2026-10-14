package catalog

import (
	"strings"
	"testing"
)

const goodSHA = "3c508e740a2f47d813cba000ff3a17519cfeb43fc9e66a82f5e862686c53130e"

func TestParseRepo(t *testing.T) {
	good := `{"format":1,"name":"Example Mods","description":"d","homepage":"https://example.com","icon":"https://example.com/i.png",
		"loader":{"version":"99.0.0","url":"https://evil.example/x.zip","sha256":"` + goodSHA + `"},
		"extra":"ignored",
		"mods":[{"id":"cool_hud","name":"Cool HUD","version":"1.2.0","url":"https://example.com/dl/cool_hud.zip","sha256":"` + goodSHA + `",
		"size":12345,"tags":["HUD"],"notes":["new"],"unknown":true}]}`
	c, err := ParseRepo([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Example Mods" || len(c.Mods) != 1 || c.Mods[0].Notes[0] != "new" || c.Loader.Version != "" {
		t.Fatalf("%+v", c)
	}
	if c, err := ParseRepo([]byte(`{"format":1,"name":"Empty","mods":[]}`)); err != nil || len(c.Mods) != 0 {
		t.Fatalf("empty repository: %v", err)
	}
	mod := func(fields string) string {
		return `{"format":1,"name":"X","mods":[{` + fields + `}]}`
	}
	base := `"name":"N","version":"1","url":"https://example.com/a.zip","sha256":"` + goodSHA + `"`
	bad := map[string]string{
		"not json":      `{`,
		"no format":     `{"name":"X","mods":[]}`,
		"format 2":      `{"format":2,"name":"X","mods":[]}`,
		"no name":       `{"format":1,"mods":[]}`,
		"no mods":       `{"format":1,"name":"X"}`,
		"null mods":     `{"format":1,"name":"X","mods":null}`,
		"bad id upper":  mod(`"id":"Cool",` + base),
		"bad id slash":  mod(`"id":"a/b",` + base),
		"bad id long":   mod(`"id":"` + strings.Repeat("a", 65) + `",` + base),
		"no version":    mod(`"id":"a","name":"N","url":"https://example.com/a.zip","sha256":"` + goodSHA + `"`),
		"no sha":        mod(`"id":"a","name":"N","version":"1","url":"https://example.com/a.zip"`),
		"short sha":     mod(`"id":"a","name":"N","version":"1","url":"https://example.com/a.zip","sha256":"abc"`),
		"http url":      mod(`"id":"a","name":"N","version":"1","url":"http://example.com/a.zip","sha256":"` + goodSHA + `"`),
		"wrong types":   `{"format":"1","name":"X","mods":[]}`,
		"duplicate ids": `{"format":1,"name":"X","mods":[{"id":"a",` + base + `},{"id":"a",` + base + `}]}`,
	}
	for name, doc := range bad {
		if _, err := ParseRepo([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRepoURLsAndIDs(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/repo.json": true, "http://localhost:8080/r.json": true, "http://127.0.0.1/r.json": true,
		"http://example.com/r.json": false, "ftp://example.com/r.json": false, "https://user:pw@example.com/r": false, "repo.json": false,
	} {
		if _, err := CheckURL(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
	u1, _ := CheckURL("https://Mods.Example.com:8443/a/repo.json")
	u2, _ := CheckURL("https://mods.example.com:8443/b/repo.json")
	id1 := RepoID(u1)
	if id1 != RepoID(u1) || id1 == RepoID(u2) || !strings.HasPrefix(id1, "mods.example.com_8443-") || !repoIDRe.MatchString(id1) {
		t.Fatalf("ids: %s %s", id1, RepoID(u2))
	}
	for id, ok := range map[string]bool{"uikit": true, "gb-12": true, id1 + "/cool_hud": true, id1 + "/Cool": false,
		"../x/y": false, "a/b/c": false, id1 + "/": false, "/x": false} {
		if ValidModID(id) != ok {
			t.Errorf("ValidModID(%q) want %v", id, ok)
		}
	}
}
