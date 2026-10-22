//go:build !windows

package winui

// Non-Windows stubs, so everything builds and tests on Linux (the page can be
// opened in a browser during development).

import (
	"log"
)

func RunUI(a Host) { <-a.Done() }

// Without WebView2 (development on Linux) the page is opened by hand.
func ShowWindow(a Host) { log.Printf("window: open %s in a browser", a.OpenURL()) }

func WindowOpen() bool { return false }

func AllowForeground(pid int) {}

func QuitUI() {}

// notify shows a desktop notification; on Linux (development) it only logs.
func Notify(title, msg, nav string) { log.Printf("notification: %s: %s", title, msg) }

func NotifyAlert(title, msg string) { log.Printf("notification: %s: %s", title, msg) }
