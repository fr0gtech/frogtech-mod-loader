//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

// Non-Windows stubs so the package builds and tests on Linux.

func steamRootsFromRegistry() []string {
	if home, err := os.UserHomeDir(); err == nil {
		return []string{home + "/.local/share/Steam", home + "/.steam/steam"}
	}
	return nil
}

func isGameRunning() bool { return false }

func openURL(url string) error { return exec.Command("xdg-open", url).Start() }

func openFolder(dir string) error { return exec.Command("xdg-open", dir).Start() }

func setRunAtStartup(on bool, command string) error { return nil }

func getRunAtStartup() bool { return false }

func registerProtocol(exe string) error { return nil }

func showError(title, msg string) { fmt.Fprintln(os.Stderr, title+": "+msg) }

func runUI(a *App) { <-a.quit }

// Without WebView2 (development on Linux) the page is opened by hand.
func showWindow(a *App) { log.Printf("window: open %s in a browser", a.OpenURL()) }

func windowOpen() bool { return false }

func allowForeground(pid int) {}

func askYesNo(title, msg string) bool { return false }

func quitUI() {}

// notify shows a desktop notification; on Linux (development) it only logs.
func notify(title, msg, nav string) { log.Printf("notification: %s: %s", title, msg) }
