package steam

import (
	"os"
	"path/filepath"
	"testing"
)

const id64Base = 76561197960265728

// fakeSteam writes config/loginusers.vdf and one localconfig.vdf per account
// (accountID → Deadlock's launch options; "" leaves them out).
func fakeSteam(t *testing.T, loginusers string, opts map[string]string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "loginusers.vdf"), []byte(loginusers), 0o644)
	for acct, o := range opts {
		dir := filepath.Join(root, "userdata", acct, "config")
		os.MkdirAll(dir, 0o755)
		app := ""
		if o != "" {
			app = "\t\t\t\t\t\"LaunchOptions\"\t\t\"" + o + "\"\n"
		}
		vdf := "\"UserLocalConfigStore\"\n{\n\t\"Software\"\n\t{\n\t\t\"Valve\"\n\t\t{\n\t\t\t\"Steam\"\n\t\t\t{\n\t\t\t\t\"apps\"\n\t\t\t\t{\n" +
			"\t\t\t\t\t\"1422450\"\n\t\t\t\t\t{\n\t\t\t\t\t\t\"LastPlayed\"\t\t\"1792652131\"\n" + app + "\t\t\t\t\t}\n" +
			"\t\t\t\t}\n\t\t\t}\n\t\t}\n\t}\n}\n"
		os.WriteFile(filepath.Join(dir, "localconfig.vdf"), []byte(vdf), 0o644)
	}
	return root
}

// two accounts: 43593840 (newer timestamp) and 142480948
const twoUsers = `"users"
{
	"76561198003859568"
	{
		"AccountName"		"a"
		"timestamp"		"1792435185"
	}
	"76561198102746676"
	{
		"AccountName"		"b"
		"timestamp"		"1792000000"
	}
}
`

func TestLaunchOptionsNewestUser(t *testing.T) {
	root := fakeSteam(t, twoUsers, map[string]string{"43593840": "-condebug -novid", "142480948": ""})
	opts, ok := launchOptionsIn(root)
	if !ok || opts != "-condebug -novid" {
		t.Fatalf("launchOptionsIn = %q, %v; want the newest account's options", opts, ok)
	}
}

func TestLaunchOptionsMostRecentWins(t *testing.T) {
	users := `"users"
{
	"76561198003859568"
	{
		"timestamp"		"1792435185"
	}
	"76561198102746676"
	{
		"MostRecent"		"1"
		"timestamp"		"1792000000"
	}
}
`
	root := fakeSteam(t, users, map[string]string{"43593840": "-condebug", "142480948": "-novid"})
	if opts, ok := launchOptionsIn(root); !ok || opts != "-novid" {
		t.Fatalf("launchOptionsIn = %q, %v; want the MostRecent account's", opts, ok)
	}
}

func TestLaunchOptionsNoneSet(t *testing.T) {
	root := fakeSteam(t, twoUsers, map[string]string{"43593840": "", "142480948": "-condebug"})
	if opts, ok := launchOptionsIn(root); !ok || opts != "" {
		t.Fatalf("launchOptionsIn = %q, %v; want \"\", true (read, none set)", opts, ok)
	}
}

func TestLaunchOptionsNoSteam(t *testing.T) {
	if _, ok := launchOptionsIn(t.TempDir()); ok {
		t.Fatal("launchOptionsIn on an empty folder = ok, want not found")
	}
}

func TestHasCondebug(t *testing.T) {
	for opts, want := range map[string]bool{
		"-condebug":                 true,
		"-novid -condebug +fps_max": true,
		"-CONDEBUG":                 true,
		"":                          false,
		"-condebugx":                false,
		"-novid":                    false,
		"-dev -condebug2":           false,
	} {
		if got := HasCondebug(opts); got != want {
			t.Errorf("HasCondebug(%q) = %v, want %v", opts, got, want)
		}
	}
}
