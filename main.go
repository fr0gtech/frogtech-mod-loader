// Command FrogLoader is the Frogtech Mod Loader for Deadlock: it keeps
// gameinfo.gi patched, installs and auto-updates mods, and serves its UI as a
// local web page.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.5.0-dev"

func dataDirPath() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "FrogLoader")
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "FrogLoader")
	}
	return "FrogLoader"
}

func setupLog(dataDir string) {
	p := filepath.Join(dataDir, "loader.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.SetOutput(io.MultiWriter(uiLog, os.Stderr))
		return
	}
	log.SetOutput(io.MultiWriter(f, uiLog, os.Stderr))
	log.SetFlags(log.Ldate | log.Ltime)
}

func main() {
	background := flag.Bool("background", false, "start in the tray without opening the window (used at Windows startup)")
	flag.Bool("no-browser", false, "ignored (accepted for 0.1.0 compatibility)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	link := flag.String("url", "", "handle a frogmod:// link (set by the URL protocol registration)")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	dataDir := dataDirPath()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal(err)
	}
	setupLog(dataDir)
	token := loadToken(dataDir)

	// Second launch: bring the running instance's window to the front and exit.
	if base, pid := findRunning(dataDir, token); base != "" {
		switch {
		case *link != "":
			forwardURL(base, pid, token, *link)
		case !*background:
			showRunning(base, pid, token)
		}
		return
	}

	log.Printf("Frogtech Mod Loader %s starting (background=%v)", version, *background)
	go cleanupOldExe()
	os.RemoveAll(filepath.Join(dataDir, "tmp"))

	app, err := NewApp(dataDir)
	if err != nil {
		log.Printf("state: %v", err)
	}
	app.token = token
	app.background = *background
	initRunKey(app)
	if exe, err := os.Executable(); err == nil {
		if err := registerProtocol(exe); err != nil {
			log.Printf("frogmod:// registration: %v", err)
		}
	}
	if err := app.StartServer(); err != nil {
		fatal(err)
	}
	app.RunBackground()
	switch {
	case *link != "":
		go app.HandleURL(context.Background(), *link) // shows the window itself
	case !*background:
		app.ShowWindow()
	}
	runUI(app) // blocks until Quit (tray loop on Windows)
	stopServer()
	os.Remove(instancePath(dataDir))
	log.Printf("stopped")
}

// initRunKey applies the start-with-Windows default once and keeps the
// registered path pointing at the current exe.
func initRunKey(a *App) {
	a.mu.Lock()
	first := !a.state.RunKeyInit
	on := a.state.Settings.StartWithWindows
	a.state.RunKeyInit = true
	a.saveLocked()
	a.mu.Unlock()
	if first || on {
		applyRunKey(on)
	}
}

func fatal(err error) {
	log.Printf("fatal: %v", err)
	showError("Frogtech Mod Loader", err.Error())
	os.Exit(1)
}
