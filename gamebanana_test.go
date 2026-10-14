package main

import (
	"strings"
	"testing"
)

func TestParseGameBananaURL(t *testing.T) {
	type want struct {
		model string
		id    int
	}
	ok := map[string]want{
		"https://gamebanana.com/mods/650634":          {ModelMod, 650634},
		"gamebanana.com/mods/650634?foo=1":            {ModelMod, 650634},
		"https://gamebanana.com/mods/download/650634": {ModelMod, 650634},
		"https://gamebanana.com/sounds/83198":         {ModelSound, 83198},
		" 12345 ":                                     {ModelMod, 12345},
	}
	for in, w := range ok {
		if m, id, err := ParseGameBananaURL(in); err != nil || m != w.model || id != w.id {
			t.Errorf("%q: %s %d %v", in, m, id, err)
		}
	}
	for _, in := range []string{"", "https://gamebanana.com/dl/1847582", "https://example.com/mods/1", "abc"} {
		if _, _, err := ParseGameBananaURL(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if gbModID(ModelMod, 5) != "gb-5" || gbModID(ModelSound, 5) != "gb-sound-5" {
		t.Error("mod ids changed (0.1.0 state compatibility)")
	}
}

// testdata/gb_*.json are trimmed real API v11 responses (October 2026).
func TestParseGBMod(t *testing.T) {
	m, err := parseGBMod([]byte(readTestdata(t, "gb_mod.json")), ModelMod, 650634)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name == "" || m.Version != "4.0.5" || m.DateModified == 0 || m.Game.ID != gbDeadlockGame {
		t.Fatalf("%+v", m)
	}
	f, ok := m.Newest()
	if !ok || f.ID != 1847582 || f.URL != "https://gamebanana.com/dl/1847582" || f.MD5 == "" || f.Size == 0 {
		t.Fatalf("newest = %+v", f)
	}
	if len(m.ActiveFiles()) != 1 {
		t.Fatalf("archived files should be hidden: %d", len(m.ActiveFiles()))
	}
	inst := &Mod{GBFileID: 1847582}
	if _, up := GBUpdateAvailable(inst, m); up {
		t.Fatal("same file reported as update")
	}
	inst.GBFileID = 1
	if f, up := GBUpdateAvailable(inst, m); !up || f.ID != 1847582 {
		t.Fatal("update not detected")
	}
	inst.GBPinned = true
	if _, up := GBUpdateAvailable(inst, m); up {
		t.Fatal("pinned mod updated")
	}
	if _, err := parseGBMod([]byte(`{"_sName":"x","_aGame":{"_idRow":1,"_sName":"Other"}}`), ModelMod, 1); err == nil {
		t.Fatal("non-Deadlock mod accepted")
	}
	if _, err := parseGBMod([]byte(`{"_sErrorCode":"INPUT_ERRORS","_aErrorData":{}}`), ModelMod, 1); err == nil {
		t.Fatal("API error not detected")
	}
}

func TestBrowseParsing(t *testing.T) {
	res, err := normalizeList([]byte(readTestdata(t, "gb_index.json")), ModelMod, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 3 || res.Total < 1000 || !res.More {
		t.Fatalf("%+v", res)
	}
	it := res.Items[0]
	if it.ID == 0 || it.Name == "" || it.Author == "" || it.Likes == 0 || !strings.HasPrefix(it.Thumb, "https://images.gamebanana.com/") ||
		it.Category == "" || it.Page == "" || it.Added == 0 {
		t.Fatalf("item: %+v", it)
	}
	res, err = normalizeList([]byte(readTestdata(t, "gb_search_sound.json")), ModelSound, 1)
	if err != nil || len(res.Items) != 2 || res.Items[0].Model != ModelSound {
		t.Fatalf("sound search: %+v %v", res, err)
	}

	p := browsePath(BrowseQuery{Model: "Mod", Sort: "downloaded", Category: 31713, Page: 2})
	for _, want := range []string{"/Mod/Index?", "Generic_Game%5D=20948", "Generic_Category%5D=31713", "_sSort=Generic_MostDownloaded", "_nPage=2"} {
		if !strings.Contains(p, want) {
			t.Errorf("%s missing %s", p, want)
		}
	}
	p = browsePath(BrowseQuery{Model: "Sound", Sort: "newest", Search: "drifter ult"})
	for _, want := range []string{"/Util/Search/Results?", "_sModelName=Sound", "_idGameRow=20948", "_sOrder=date", "_sSearchString=drifter+ult"} {
		if !strings.Contains(p, want) {
			t.Errorf("%s missing %s", p, want)
		}
	}
}

func TestDetailsParsing(t *testing.T) {
	d, err := normalizeDetails([]byte(readTestdata(t, "gb_profile.json")), ModelMod, 650634)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Full) != len(d.Images) || !strings.HasPrefix(d.Full[0], "https://images.gamebanana.com/") || strings.Contains(d.Full[0], "800-90_") {
		t.Fatalf("full-size images: %v", d.Full)
	}
	if d.Name == "" || d.Downloads == 0 || len(d.Images) == 0 || len(d.Files) == 0 || d.NSFW || d.Author == "" {
		t.Fatalf("%+v", d)
	}
	if strings.Contains(d.Text, "<") || !strings.Contains(d.Text, "• Uninstall all other HUD") {
		t.Fatalf("text: %q", d.Text[:200])
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText(`<b>Hi&amp;bye</b><br>line2<ul><li>a</li><li>b</li></ul><script>x()</script><p>p</p>`)
	want := "Hi&bye\nline2\n• a\n• b\nx()\np"
	if got != want {
		t.Fatalf("%q", got)
	}
}
