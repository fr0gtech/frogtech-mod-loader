// Package platform wraps what differs per OS: the game process check, opening
// links and folders, the Run-at-startup and frogmod:// registry entries, and
// message boxes. Non-Windows versions are stubs so everything builds and tests
// on Linux.
package platform

import "fmt"

// ProtocolCommand is the shell\open\command registered for frogmod://.
func ProtocolCommand(exe string) string {
	return fmt.Sprintf(`"%s" --url "%%1"`, exe)
}

// ---- events for the page (toasts, "open this mod") ----
