//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyValue = "FrogLoader"
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

// isGameRunning looks for deadlock.exe with a Toolhelp process snapshot.
func isGameRunning() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), "deadlock.exe") {
			return true
		}
	}
	return false
}

// openURL opens an http(s) or steam:// link with the default handler.
func openURL(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func openFolder(dir string) error {
	return exec.Command("explorer.exe", dir).Start()
}

func setRunAtStartup(on bool, command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runKeyValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	if cur, _, err := k.GetStringValue(runKeyValue); err == nil && cur == command {
		return nil
	}
	return k.SetStringValue(runKeyValue, command)
}

// registerProtocol registers frogmod:// for this user (HKCU\Software\Classes),
// rewriting it whenever the exe has moved.
func registerProtocol(exe string) error {
	base := `Software\Classes\` + urlScheme
	values := []struct{ path, name, value string }{
		{base, "", "URL:Frogtech Mod Loader"},
		{base, "URL Protocol", ""},
		{base + `\DefaultIcon`, "", fmt.Sprintf(`"%s",0`, exe)},
		{base + `\shell\open\command`, "", protocolCommand(exe)},
	}
	for _, v := range values {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, v.path, registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			return err
		}
		var setErr error
		if cur, _, gerr := k.GetStringValue(v.name); gerr != nil || cur != v.value {
			setErr = k.SetStringValue(v.name, v.value)
		}
		k.Close()
		if setErr != nil {
			return setErr
		}
	}
	return nil
}

func getRunAtStartup() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runKeyValue)
	return err == nil
}

func showError(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

func askYesNo(title, msg string) bool {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	r, _ := windows.MessageBox(0, m, t, windows.MB_YESNO|windows.MB_ICONINFORMATION)
	return r == 6 // IDYES
}

// quitUI closes the window and the tray icon (runUI then returns).
func quitUI() {
	closeWindow()
	closeTray()
}
