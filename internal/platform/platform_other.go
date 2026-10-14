//go:build !windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
)

func IsGameRunning() bool { return false }

func OpenURL(url string) error { return exec.Command("xdg-open", url).Start() }

func OpenFolder(dir string) error { return exec.Command("xdg-open", dir).Start() }

func SetRunAtStartup(on bool, command string) error { return nil }

func GetRunAtStartup() bool { return false }

func RegisterProtocol(exe string) error { return nil }

func ShowError(title, msg string) { fmt.Fprintln(os.Stderr, title+": "+msg) }

func AskYesNo(title, msg string) bool { return false }
