// Package winui is the Windows user interface around the web page: a
// frameless Win32 window hosting WebView2 with the page's own title bar, the
// tray icon with its menu and notifications. Elsewhere it's a stub (the page
// can be opened in a browser during development).
package winui

import "github.com/fr0gtech/frogtech-mod-loader/internal/mods"

// Host is what the window and tray need from the app.
type Host interface {
	OpenURL() string                // the page address (carries the token once)
	DataDir() string                // %LOCALAPPDATA%\FrogLoader (WebView2 profile)
	KeepInTray() bool               // closing the window keeps the app running
	WindowRect() *mods.WindowRect   // last window position, or nil
	SetWindowRect(*mods.WindowRect) // remember the window position
	ShowWindow()                    // open or focus the window
	ShowWindowAt(nav string)        // ... on a section ("updates")
	CheckFromTray()                 // the tray's "Check for updates"
	ModsOn() bool                   // the main switch, ticked in the tray menu
	SetModsOnFromTray(on bool)      // the tray's "Mods on"
	Quit()
	Done() <-chan struct{} // closed when the app quits
}
