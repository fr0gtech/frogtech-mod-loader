package steam

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// steamID64Base turns a SteamID64 into the account id used for userdata folders.
const steamID64Base = 76561197960265728

// LaunchOptions returns Deadlock's launch options for the Steam account that
// logged in last; ok is false when Steam's files couldn't be read.
func LaunchOptions() (opts string, ok bool) {
	roots := appendUnique(nil, steamRootsFromRegistry()...)
	roots = appendUnique(roots, `C:\Program Files (x86)\Steam`, `C:\Program Files\Steam`)
	for _, root := range roots {
		if opts, ok := launchOptionsIn(root); ok {
			return opts, true
		}
	}
	return "", false
}

// launchOptionsIn reads them under one Steam folder: config/loginusers.vdf says
// which account logged in last (MostRecent, else the newest timestamp), its
// userdata/<account>/config/localconfig.vdf holds the options.
func launchOptionsIn(root string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, "config", "loginusers.vdf"))
	if err != nil {
		return "", false
	}
	v, err := ParseVDF(string(data))
	if err != nil {
		return "", false
	}
	var best string
	var bestTime int64 = -1
	for id, val := range v.Sub("users") {
		u, ok := val.(VDF)
		if !ok {
			continue
		}
		ts, _ := strconv.ParseInt(u.Get("timestamp"), 10, 64)
		if u.Get("MostRecent") == "1" {
			ts = 1 << 62
		}
		if ts > bestTime {
			best, bestTime = id, ts
		}
	}
	id64, err := strconv.ParseUint(best, 10, 64)
	if err != nil || id64 < steamID64Base {
		return "", false
	}
	acct := strconv.FormatUint(id64-steamID64Base, 10)
	data, err = os.ReadFile(filepath.Join(root, "userdata", acct, "config", "localconfig.vdf"))
	if err != nil {
		return "", false
	}
	lc, err := ParseVDF(string(data))
	if err != nil {
		return "", false
	}
	return lc.Get("UserLocalConfigStore", "Software", "Valve", "Steam", "apps", AppID, "LaunchOptions"), true
}

// HasCondebug says whether launch options contain -condebug (the option that
// makes Deadlock write console.log).
func HasCondebug(opts string) bool {
	for _, f := range strings.Fields(opts) {
		if strings.EqualFold(f, "-condebug") {
			return true
		}
	}
	return false
}
