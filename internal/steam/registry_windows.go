//go:build windows

package steam

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

func steamRootsFromRegistry() []string {
	var out []string
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("InstallPath"); err == nil && v != "" {
			out = append(out, v)
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("SteamPath"); err == nil && v != "" {
			out = append(out, strings.ReplaceAll(v, "/", `\`))
		}
		k.Close()
	}
	return out
}
