//go:build !windows

package steam

import "os"

func steamRootsFromRegistry() []string {
	if home, err := os.UserHomeDir(); err == nil {
		return []string{home + "/.local/share/Steam", home + "/.steam/steam"}
	}
	return nil
}
