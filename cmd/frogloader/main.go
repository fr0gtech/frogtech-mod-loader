// Command frogloader is Frogtech Mod Loader (FrogLoader.exe), a mod loader and
// auto-updater for Deadlock. This file only wires the packages together:
// internal/app does the work, internal/server serves the page, internal/winui
// shows it in a window with a tray icon.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/fr0gtech/frogtech-mod-loader/internal/app"
	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/fr0gtech/frogtech-mod-loader/internal/server"
	"github.com/fr0gtech/frogtech-mod-loader/internal/winui"
)

// version is set at build time with -ldflags "-X main.version=..." (build.sh).
var version = "0.6.1-dev"

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
		log.SetOutput(io.MultiWriter(app.UILog, os.Stderr))
		return
	}
	log.SetOutput(io.MultiWriter(f, app.UILog, os.Stderr))
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
	app.Version = version
	fetch.UserAgent = "FrogLoader/" + version
	token := server.LoadToken(dataDir)

	// Second launch: bring the running instance's window to the front and exit.
	if base, pid := server.FindRunning(dataDir, token); base != "" {
		switch {
		case *link != "":
			server.ForwardURL(base, pid, token, *link)
		case !*background:
			server.ShowRunning(base, pid, token)
		}
		return
	}

	log.Printf("Frogtech Mod Loader %s starting (background=%v)", version, *background)
	go app.CleanupOldExe()
	os.RemoveAll(filepath.Join(dataDir, "tmp"))

	a, err := app.NewApp(dataDir)
	if err != nil {
		log.Printf("state: %v", err)
	}
	a.SetToken(token)
	a.SetBackground(*background)
	a.StopServer = server.Stop
	a.StartServer = func() error { return server.Start(a) }
	a.InitRunKey()
	if exe, err := os.Executable(); err == nil {
		if err := platform.RegisterProtocol(exe); err != nil {
			log.Printf("frogmod:// registration: %v", err)
		}
	}
	if err := server.Start(a); err != nil {
		fatal(err)
	}
	a.RunBackground()
	switch {
	case *link != "":
		go a.HandleURL(context.Background(), *link) // shows the window itself
	case !*background:
		a.ShowWindow()
	}
	winui.RunUI(a) // blocks until Quit (tray loop on Windows)
	server.Stop()
	os.Remove(server.InstancePath(dataDir))
	log.Printf("stopped")
}

func fatal(err error) {
	log.Printf("fatal: %v", err)
	platform.ShowError("Frogtech Mod Loader", err.Error())
	os.Exit(1)
}
