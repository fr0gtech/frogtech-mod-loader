package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/crashdump"
	"github.com/fr0gtech/frogtech-mod-loader/internal/gameinfo"
	"github.com/fr0gtech/frogtech-mod-loader/internal/steam"
	"github.com/fr0gtech/frogtech-mod-loader/internal/testutil"
)

// fakeGame swaps the game process check, steam:// links and the clock.
type fakeGame struct {
	mu      sync.Mutex
	running bool
	opened  []string
	t       time.Time
}

func newFakeGame(t *testing.T) *fakeGame {
	f := &fakeGame{t: time.Now()}
	oldRun, oldOpen, oldNow := gameRunning, openURL, now
	gameRunning = func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.running }
	openURL = func(u string) error { f.mu.Lock(); defer f.mu.Unlock(); f.opened = append(f.opened, u); return nil }
	now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
	t.Cleanup(func() { gameRunning, openURL, now = oldRun, oldOpen, oldNow })
	return f
}

func (f *fakeGame) set(running bool)     { f.mu.Lock(); f.running = running; f.mu.Unlock() }
func (f *fakeGame) wait(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }
func (f *fakeGame) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.opened) == 0 {
		return ""
	}
	return f.opened[len(f.opened)-1]
}

// setFlags rewrites StateFlags (and LastUpdated) in the fake appmanifest.
func setFlags(t *testing.T, game, flags, lastUpdated string) {
	t.Helper()
	p := steam.ManifestPath(game)
	data := testutil.Read(t, "appmanifest_1422450.acf")
	data = strings.Replace(data, `"StateFlags"		"4"`, `"StateFlags"		"`+flags+`"`, 1)
	if lastUpdated != "" {
		data = strings.Replace(data, `"LastUpdated"		"1791927344"`, `"LastUpdated"		"`+lastUpdated+`"`, 1)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDump writes a crash dump like the game's, dated at.
func writeDump(t *testing.T, game, name string, at time.Time, msg string) {
	t.Helper()
	dir := crashdump.Dir(game)
	os.MkdirAll(dir, 0o755)
	body := []byte("MDMP\x93\xa7\x00\x00AbortMessage({FATAL ERROR: " + msg + "\x00\x00\xff")
	p := filepath.Join(dir, name)
	os.WriteFile(p, body, 0o644)
	os.Chtimes(p, at, at)
}

func gameinfoText(t *testing.T, game string) string {
	_, _, p := steam.Paths(game)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setupGameWithMod(t *testing.T) (*App, string, string, *fakeGame) {
	t.Helper()
	f := newFakeGame(t)
	fakeCatalog(t)
	_, game := testutil.FakeLibrary(t)
	_, addons, _ := steam.Paths(game)
	a := testApp(t)
	if err := a.SetGameDir(game); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.RefreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallCatalogMod(ctx, "uikit"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(addons, "pak01_dir.vpk")); err != nil {
		t.Fatal("mod not applied")
	}
	return a, game, addons, f
}

func TestModsSwitch(t *testing.T) {
	a, game, addons, f := setupGameWithMod(t)
	stock := testutil.Read(t, "gameinfo_stock_6785.gi")
	if st := a.Status(); !st.ModsOn || !st.GameinfoOK || !st.Gameinfo.Patched() {
		t.Fatalf("patched on install: %+v", st.Gameinfo)
	}
	pak := filepath.Join(addons, "pak01_dir.vpk")
	foreign := filepath.Join(addons, "pak42_dir.vpk")
	os.WriteFile(foreign, testutil.FakeVPK("someone else's"), 0o644)

	if err := a.SetModsOn(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pak); !os.IsNotExist(err) {
		t.Fatal("our pak still in the game with mods off")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("another program's file was touched")
	}
	if gameinfoText(t, game) != stock {
		t.Fatal("gameinfo.gi isn't Valve's file with mods off")
	}
	st := a.Status()
	if st.ModsOn || !st.GameinfoOK || st.Gameinfo.Addons {
		t.Fatalf("status off: on=%v ok=%v %+v", st.ModsOn, st.GameinfoOK, st.Gameinfo)
	}
	// The watcher keeps it that way.
	if _, err := a.EnsureGameinfo(); err != nil || gameinfoText(t, game) != stock {
		t.Fatal("watcher patched gameinfo with mods off", err)
	}
	// Persisted.
	b, _ := os.ReadFile(a.statePath)
	if !strings.Contains(string(b), `"modsOff": true`) {
		t.Fatal("switch not saved")
	}

	os.Remove(foreign)
	if err := a.SetModsOn(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pak); err != nil {
		t.Fatal("mod not back after switching on")
	}
	if r, _ := gameinfo.Status(game); !r.Patched() {
		t.Fatal("gameinfo not patched after switching on")
	}

	// Switching off while the game runs: gameinfo now, files after it closes.
	f.set(true)
	if err := a.SetModsOn(false); err != nil {
		t.Fatal(err)
	}
	if gameinfoText(t, game) != stock {
		t.Fatal("gameinfo should change right away")
	}
	if _, err := os.Stat(pak); err != nil {
		t.Fatal("files are locked while the game runs")
	}
	f.set(false)
	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pak); !os.IsNotExist(err) {
		t.Fatal("pending switch-off not applied after the game closed")
	}
}

// The user's crash loop: Steam's file check gave back a stock gameinfo.gi,
// the old loader added only the addons line, the game stopped with "Unable
// to read default keybinding configuration".
func TestHalfPatchedFileIsFixed(t *testing.T) {
	a, game, _, _ := setupGameWithMod(t)
	a.mu.Lock()
	a.state.Settings.KeepGameinfo = false
	a.mu.Unlock()
	stock := testutil.Read(t, "gameinfo_stock_6785.gi")
	half := strings.Replace(stock, "\t\t\tGame\t\t\t\tcitadel\r\n", "\t\t\tGame\t\t\t\tcitadel/addons\r\n\t\t\tGame\t\t\t\tcitadel\r\n", 1)
	_, _, p := steam.Paths(game)
	os.WriteFile(p, []byte(half), 0o644)
	if st := a.Status(); st.GameinfoOK {
		t.Fatal("a file without Mod/Write must be reported as needing a fix")
	}
	if !a.halfPatched() {
		t.Fatal("watcher wouldn't fix it with the setting off")
	}
	if _, err := a.EnsureGameinfo(); err != nil {
		t.Fatal(err)
	}
	if st := a.Status(); !st.GameinfoOK {
		t.Fatalf("not fixed: %+v", st.Gameinfo)
	}
	// While Steam is working on the game the file is left alone.
	os.WriteFile(p, []byte(stock), 0o644)
	setFlags(t, game, "131076", "")
	if changed, _ := a.EnsureGameinfo(); changed || gameinfoText(t, game) != stock {
		t.Fatal("patched while Steam checks the files")
	}
}

func TestStartFailAndRepair(t *testing.T) {
	a, game, addons, f := setupGameWithMod(t)
	pak := filepath.Join(addons, "pak01_dir.vpk")
	start := f.t

	// Old dumps and crashes long after a launch don't count.
	writeDump(t, game, "deadlock_2026_1001_000000_0_error.mdmp", start.Add(-48*time.Hour), "old")
	writeDump(t, game, "deadlock_2026_1015_040000_0_accessviolation.mdmp", start.Add(-time.Hour), "")
	a.watchStart()
	if st := a.Status(); st.StartFail != nil {
		t.Fatalf("old dumps shown: %+v", st.StartFail)
	}

	if err := a.Play(false); err != nil || f.last() != "steam://rungameid/1422450" {
		t.Fatal(err, f.last())
	}
	f.wait(15 * time.Second)
	writeDump(t, game, "deadlock_2026_1015_051540_0_error.mdmp", f.t, "Unable to read default keybinding configuration (user_keys_default)!")
	setFlags(t, game, "132", "")
	a.watchStart()
	sf := a.Status().StartFail
	if sf == nil || sf.Message != "Unable to read default keybinding configuration (user_keys_default)!" || !sf.Corrupt || sf.WithoutMods {
		t.Fatalf("start fail: %+v", sf)
	}

	// Repair: mods out, Valve's gameinfo, Steam asked to check the files.
	if err := a.Repair(); err != nil {
		t.Fatal(err)
	}
	if f.last() != "steam://validate/1422450" {
		t.Fatal("Steam not asked to check the files:", f.last())
	}
	if _, err := os.Stat(pak); !os.IsNotExist(err) {
		t.Fatal("mod files still in the game during repair")
	}
	if gameinfoText(t, game) != testutil.Read(t, "gameinfo_stock_6785.gi") {
		t.Fatal("gameinfo not Valve's during repair")
	}
	st := a.Status()
	if st.Repair == nil || st.Repair.Checking || st.StartFail != nil {
		t.Fatalf("repair view: %+v %+v", st.Repair, st.StartFail)
	}
	if err := a.Play(false); err != ErrRepairing {
		t.Fatal("play during repair:", err)
	}
	// Still flagged, Steam hasn't started: keep waiting, don't patch.
	a.watchStart()
	if a.Status().Repair == nil {
		t.Fatal("repair ended before Steam did anything")
	}
	// Steam checks the files (and puts its own gameinfo.gi back).
	setFlags(t, game, "131078", "")
	a.watchStart()
	if st := a.Status(); st.Repair == nil || !st.Repair.Checking {
		t.Fatal("didn't notice Steam checking")
	}
	if changed, _ := a.EnsureGameinfo(); changed {
		t.Fatal("gameinfo patched while Steam checks")
	}
	setFlags(t, game, "4", "1792033994")
	a.watchStart()
	st = a.Status()
	if st.Repair != nil || st.StartFail != nil {
		t.Fatalf("repair not finished: %+v %+v", st.Repair, st.StartFail)
	}
	if _, err := os.Stat(pak); err != nil {
		t.Fatal("mods not back after repair")
	}
	if !st.Gameinfo.Patched() {
		t.Fatal("gameinfo not patched after repair")
	}
	// The old dump is not shown again.
	a.watchStart()
	if a.Status().StartFail != nil {
		t.Fatal("old dump shown after repair")
	}
}

func TestRepairSkipWaiting(t *testing.T) {
	a, game, addons, _ := setupGameWithMod(t)
	if err := a.Repair(); err != nil {
		t.Fatal(err)
	}
	if err := a.SetModsOn(false); err != ErrRepairing {
		t.Fatal("switch during repair:", err)
	}
	// The state survives a restart of the loader.
	b, _ := os.ReadFile(a.statePath)
	if !strings.Contains(string(b), `"repair"`) {
		t.Fatal("repair not saved")
	}
	if err := a.FinishRepair(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(addons, "pak01_dir.vpk")); err != nil {
		t.Fatal("mods not back")
	}
	if r, _ := gameinfo.Status(game); !r.Patched() {
		t.Fatal("gameinfo not patched")
	}
}

func TestPlayWithoutMods(t *testing.T) {
	a, game, addons, f := setupGameWithMod(t)
	stock := testutil.Read(t, "gameinfo_stock_6785.gi")
	pak := filepath.Join(addons, "pak01_dir.vpk")
	if err := a.PlayWithoutMods(); err != nil || f.last() != "steam://rungameid/1422450" {
		t.Fatal(err, f.last())
	}
	if gameinfoText(t, game) != stock {
		t.Fatal("gameinfo not Valve's for a start without mods")
	}
	if _, err := os.Stat(pak); err != nil {
		t.Fatal("mod files shouldn't be touched (they aren't loaded)")
	}
	if _, err := a.EnsureGameinfo(); err != nil || gameinfoText(t, game) != stock {
		t.Fatal("watcher patched during the run")
	}
	f.wait(5 * time.Second)
	a.watchStart() // not running yet
	if !a.Status().NoModsRun {
		t.Fatal("ended before the game started")
	}
	f.set(true)
	a.watchStart()
	f.wait(10 * time.Minute)
	a.watchStart()
	if gameinfoText(t, game) != stock {
		t.Fatal("restored while the game runs")
	}
	f.set(false)
	a.watchStart()
	if a.Status().NoModsRun {
		t.Fatal("not ended after the game closed")
	}
	if r, _ := gameinfo.Status(game); !r.Patched() {
		t.Fatal("gameinfo not patched again")
	}

	// It fails without mods too: the banner says so.
	if err := a.PlayWithoutMods(); err != nil {
		t.Fatal(err)
	}
	f.wait(10 * time.Second)
	writeDump(t, game, "deadlock_2026_1015_060000_0_error.mdmp", f.t, "Application unable to load gameinfo.gi file from directory \"citadel\"")
	f.wait(3*time.Minute + time.Second) // never seen running: give up waiting
	a.watchStart()
	st := a.Status()
	if st.NoModsRun || st.StartFail == nil || !st.StartFail.WithoutMods || !strings.Contains(st.StartFail.Message, "gameinfo.gi") {
		t.Fatalf("no-mods failure: run=%v %+v", st.NoModsRun, st.StartFail)
	}
	if err := a.DismissStartFail(); err != nil {
		t.Fatal(err)
	}
	a.watchStart()
	if a.Status().StartFail != nil {
		t.Fatal("dismissed banner came back")
	}
}

func TestStartFailOutsideLoader(t *testing.T) {
	a, game, _, f := setupGameWithMod(t)
	// A crash (not an error) with no launch from the loader isn't a failed start.
	writeDump(t, game, "deadlock_2026_1015_050000_0_accessviolation.mdmp", f.t.Add(time.Second), "")
	a.watchStart()
	if a.Status().StartFail != nil {
		t.Fatal("a crash mid-game shown as a failed start")
	}
	// A fatal error from a launch through Steam is.
	f.wait(time.Minute)
	writeDump(t, game, "deadlock_2026_1015_050100_0_error.mdmp", f.t, "Unable to read default keybinding configuration (user_keys_default)!")
	a.watchStart()
	if sf := a.Status().StartFail; sf == nil || sf.Corrupt {
		t.Fatalf("%+v", sf)
	}
	// The game then runs fine for a while: the banner goes.
	f.wait(time.Second)
	f.set(true)
	a.watchStart()
	f.wait(2 * time.Minute)
	a.watchStart()
	if a.Status().StartFail != nil {
		t.Fatal("banner stays after a good start")
	}
	// Steam's damaged flag alone (game not running) shows the banner too.
	f.set(false)
	setFlags(t, game, "132", "")
	a.watchStart()
	if sf := a.Status().StartFail; sf == nil || !sf.Corrupt || sf.Message != "" {
		t.Fatalf("corrupt flag: %+v", sf)
	}
	a.DismissStartFail()
	a.watchStart()
	if a.Status().StartFail != nil {
		t.Fatal("dismissed corrupt flag came back")
	}
}
