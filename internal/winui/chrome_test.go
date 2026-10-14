package winui

import (
	"os"
	"regexp"
	"testing"
)

func TestHitTest(t *testing.T) {
	// window at (100,50)-(900,650), 6px border
	cases := []struct {
		x, y int
		max  bool
		want int
	}{
		{100, 50, false, htTopLeft}, {899, 50, false, htTopRight}, {100, 649, false, htBottomLeft}, {899, 649, false, htBottomRight},
		{103, 300, false, htLeft}, {897, 300, false, htRight}, {500, 52, false, htTop}, {500, 646, false, htBottom},
		{500, 300, false, htClient}, {106, 56, false, htClient}, {50, 300, false, htClient}, {100, 50, true, htClient},
		{500, 646, true, htClient},
	}
	for _, c := range cases {
		if got := hitTest(c.x, c.y, 100, 50, 900, 650, 6, c.max); got != c.want {
			t.Errorf("(%d,%d max=%v) = %d, want %d", c.x, c.y, c.max, got, c.want)
		}
	}
}

func TestParseWinMsg(t *testing.T) {
	ok := map[string]winMsg{
		"frog:drag": {Action: "drag"}, "frog:max": {Action: "max"}, "frog:min": {Action: "min"}, "frog:close": {Action: "close"},
		"frog:sysmenu": {Action: "sysmenu"}, "frog:state": {Action: "state"}, "frog:resize:13": {Action: "resize", Hit: htTopLeft},
		"frog:resize:17": {Action: "resize", Hit: htBottomRight},
	}
	for in, want := range ok {
		if got, good := parseWinMsg(in); !good || got != want {
			t.Errorf("%q: %+v %v", in, got, good)
		}
	}
	for _, in := range []string{"", "drag", "frog:", "frog:resize", "frog:resize:2", "frog:resize:18", "frog:resize:x", "frog:drag:1", "frog:explode", "FROG:drag"} {
		if _, good := parseWinMsg(in); good {
			t.Errorf("%q accepted", in)
		}
	}
}

// The page's resize handles must send the codes the native side expects.
func TestPageEdgeCodes(t *testing.T) {
	b, err := os.ReadFile("../server/web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"n": "12", "s": "15", "w": "10", "e": "11", "nw": "13", "ne": "14", "sw": "16", "se": "17"}
	found := regexp.MustCompile(`class="edge (\w+)" data-ht="(\d+)"`).FindAllStringSubmatch(string(b), -1)
	if len(found) != len(want) {
		t.Fatalf("found %d edges", len(found))
	}
	for _, m := range found {
		if want[m[1]] != m[2] {
			t.Errorf("edge %s sends %s, want %s", m[1], m[2], want[m[1]])
		}
	}
}
