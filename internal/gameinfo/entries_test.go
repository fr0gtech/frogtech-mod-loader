package gameinfo

import (
	"reflect"
	"testing"
)

func TestEntries(t *testing.T) {
	text := `GameInfo
{
	SceneSystem
	{
		VolumetricFog 0
	}
	ConVars
	{
		// example_line "true" // an example in the intro
		// ===== Preferences ===== \\\\
		// --- 0. IMPORTANT ---
		// mm_prefer_solo_only   "true"  // default: "false" | Solo queue only
		citadel_trooper_glow_disabled "1" // default: "0" | 1 = no trooper glow.
		r_farz 6800 // far plane
		"plain" "x"
		// --- 2. Field of View ---
		// just a sentence about things
		r_aspectratio "2.5" // 1.75=80fov | 2.15=90fov
		// r_aspectratio "1.0"
		Sub
		{
			inner 1
		}
		// --- END OF CONFIG v4 ---
		last "-6"
	}
	Other
	{
		after 1
	}
}`
	want := []Entry{
		{Key: "mm_prefer_solo_only", Value: "true", Default: "false", Note: "Solo queue only", Section: "IMPORTANT", Off: true},
		{Key: "citadel_trooper_glow_disabled", Value: "1", Default: "0", Note: "1 = no trooper glow.", Section: "IMPORTANT"},
		{Key: "r_farz", Value: "6800", Note: "far plane", Section: "IMPORTANT"},
		{Key: "plain", Value: "x", Section: "IMPORTANT"},
		{Key: "r_aspectratio", Value: "2.5", Note: "1.75=80fov | 2.15=90fov", Section: "Field of View"},
		{Key: "last", Value: "-6", Section: "Other"},
	}
	if got := Entries(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	if Entries("GameInfo { }") != nil {
		t.Fatal("entries without a ConVars block")
	}
	cfg := ConVarsConfig(map[string]string{"r_farz": "100", "bad key": "1", "plain": `a"b`}, []string{"r_farz", "bad key", "plain", "unset"})
	if cfg != "GameInfo\n{\n\tConVars\n\t{\n\t\t\"r_farz\"\t\"100\"\n\t}\n}\n" {
		t.Fatalf("%q", cfg)
	}
}
