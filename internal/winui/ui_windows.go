//go:build windows

package winui

// QuitUI closes the window and the tray icon (RunUI then returns).
func QuitUI() {
	closeWindow()
	closeTray()
}
