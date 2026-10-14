package mods

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Deadlock Mod Manager keeps its state for an addons folder in .dmm.json there. Uninstalling DMM leaves the file
// behind (an empty {"version":1,"mods":{}}), so the file alone doesn't mean DMM still manages the folder: only one
// that lists mods does. A leftover can be removed from the page.

const dmmFile = ".dmm.json"

// DMMState reports whether DMM manages the addons folder (its file lists mods) and whether only an empty leftover
// file is there.
func DMMState(addons string) (inUse, leftover bool) {
	b, err := os.ReadFile(filepath.Join(addons, dmmFile))
	if err != nil {
		return false, false
	}
	var st struct {
		Mods map[string]json.RawMessage `json:"mods"`
	}
	if json.Unmarshal(b, &st) != nil {
		return true, false // not a file we understand: assume DMM uses it
	}
	if len(st.Mods) > 0 {
		return true, false
	}
	return false, true
}

// RemoveDMMLeftover deletes an empty .dmm.json; one that lists mods is left alone.
func RemoveDMMLeftover(addons string) error {
	if inUse, leftover := DMMState(addons); inUse || !leftover {
		return nil
	}
	return os.Remove(filepath.Join(addons, dmmFile))
}
