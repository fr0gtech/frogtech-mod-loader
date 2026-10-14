package main

import (
	"errors"
	"strings"
	"testing"
)

func TestReportCheck(t *testing.T) {
	var got []string
	old := notifyHook
	notifyHook = func(title, msg, nav string) { got = append(got, title+": "+msg) }
	defer func() { notifyHook = old }()
	a := testApp(t)
	a.state.Settings.AutoUpdate = false
	take := func() []string { g := got; got = nil; return g }

	// Tray check, nothing new: always answers.
	a.reportCheck(CheckResult{Current: version}, nil, true)
	if g := take(); len(g) != 1 || !strings.HasPrefix(g[0], "You're up to date") {
		t.Fatalf("manual up to date: %v", g)
	}
	// Background check, nothing new: silent.
	a.reportCheck(CheckResult{Current: version}, nil, false)
	if g := take(); len(g) != 0 {
		t.Fatalf("background should be quiet: %v", g)
	}
	// Tray check with updates found.
	a.reportCheck(CheckResult{Loader: "9.9.9", Mods: []string{"UI Kit 0.8.2"}}, nil, true)
	if g := take(); len(g) != 1 || g[0] != "Updates found: Loader 9.9.9, UI Kit 0.8.2. Open the loader to install." {
		t.Fatalf("manual found: %v", g)
	}
	// Background: a new loader version is announced once (auto-update off).
	a.reportCheck(CheckResult{Loader: "9.9.10"}, nil, false)
	a.reportCheck(CheckResult{Loader: "9.9.10"}, nil, false)
	if g := take(); len(g) != 1 {
		t.Fatalf("loader announced %d times: %v", len(g), g)
	}
	// Background: waiting mod updates alone stay quiet; installed ones are reported.
	a.reportCheck(CheckResult{Loader: "9.9.10", Mods: []string{"X 2"}}, nil, false)
	a.reportCheck(CheckResult{Installed: []string{"UI Kit 0.8.2"}}, nil, false)
	if g := take(); len(g) != 1 || g[0] != "Mods updated: Updated: UI Kit 0.8.2." {
		t.Fatalf("installed: %v", g)
	}
	// Errors: the tray check says why; background stays quiet.
	a.reportCheck(CheckResult{}, errors.New("couldn't reach mods.frogtech.dev: timeout"), true)
	a.reportCheck(CheckResult{}, errors.New("offline"), false)
	if g := take(); len(g) != 1 || !strings.HasPrefix(g[0], "Couldn't check for updates: couldn't reach") {
		t.Fatalf("errors: %v", g)
	}
}
