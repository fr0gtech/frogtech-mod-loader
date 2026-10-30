package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0}, {"0.10.0", "0.9.9", 1}, {"1.0", "1.0.0", 0}, {"0.1.0-dev", "0.1.0", -1},
		{"0.1.1-dev", "0.1.0", 1}, {"v1.2.3", "1.2.4", -1}, {"2", "10", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestFetchCatalogAndDownload(t *testing.T) {
	payload := []byte("zip-bytes")
	sum := sha256.Sum256(payload)
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"loader":{"version":"0.2.0","url":"x","sha256":"y"},"mods":[{"id":"uikit","name":"UI Kit","version":"0.8.0","game":"6779","conflicts":["hud_fit"]}]}`)
	})
	mux.HandleFunc("/bad.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"mods":[{"id":"../evil"}]}`)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := Fetch(context.Background(), srv.URL+"/catalog.json")
	if err != nil || c.Loader.Version != "0.2.0" || c.Mod("uikit") == nil || c.Mod("uikit").Conflicts[0] != "hud_fit" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Fetch(context.Background(), srv.URL+"/bad.json"); err == nil {
		t.Fatal("path-like mod id accepted")
	}
	if _, err := Fetch(context.Background(), srv.URL+"/missing"); err == nil {
		t.Fatal("404 accepted")
	}

	dest := filepath.Join(t.TempDir(), "f")
	if err := fetch.Download(context.Background(), srv.URL+"/file", dest, fetch.Checksum{Algo: "sha256", Hex: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	if err := fetch.Download(context.Background(), srv.URL+"/file", dest, fetch.Checksum{Algo: "sha256", Hex: "00"}); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("failed download left a file")
	}
	if err := fetch.Download(context.Background(), srv.URL+"/file", dest, fetch.Checksum{Algo: "sha256", Hex: hex.EncodeToString(sum[:]), Size: 3}); err == nil {
		t.Fatal("size mismatch accepted")
	}
}

func TestCatalogTags(t *testing.T) {
	m := &Mod{ID: "uikit"}
	if got := m.TagsOf(); len(got) != 3 || got[0] != "HUD" {
		t.Fatalf("fallback tags: %v", got)
	}
	m.Tags = []string{"Tool"}
	if got := m.TagsOf(); len(got) != 1 || got[0] != "Tool" {
		t.Fatalf("catalog tags not preferred: %v", got)
	}
	if (&Mod{ID: "unknown"}).TagsOf() != nil {
		t.Fatal("unknown mod got tags")
	}
}

func TestFetchBuilds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"format": 1, "builds": {
			"6821": {"version": "1.0.0", "file": "uikit_1.0.0_g6821.zip", "sha256": "aa", "size": 5, "published": true, "built": "x"},
			"6822": {"version": "1.0.1", "file": "uikit_1.0.1_g6822.zip", "sha256": "bb", "size": 6, "published": false},
			"6823": {"version": "1.0.2", "file": "../evil.zip", "sha256": "cc", "published": true},
			"6824": {"version": "1.0.3", "file": "uikit.zip", "published": true}}}`)
	}))
	defer srv.Close()
	b, err := FetchBuilds(context.Background(), srv.URL+"/map-data/uikit/builds.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 {
		t.Fatalf("got %+v, want only 6821", b)
	}
	got := b["6821"]
	if got.Version != "1.0.0" || got.SHA256 != "aa" || got.Size != 5 || got.URL != srv.URL+"/map-data/uikit/uikit_1.0.0_g6821.zip" {
		t.Fatalf("6821: %+v", got)
	}
}
