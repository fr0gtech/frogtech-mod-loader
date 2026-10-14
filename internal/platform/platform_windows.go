//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unsafe"

	"github.com/fr0gtech/frogtech-mod-loader/internal/links"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyValue = "FrogLoader"
)

// IsGameRunning looks for deadlock.exe with a Toolhelp process snapshot.
func IsGameRunning() bool {
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

// OpenURL opens an http(s) or steam:// link with the default handler.
func OpenURL(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func OpenFolder(dir string) error {
	return exec.Command("explorer.exe", dir).Start()
}

func SetRunAtStartup(on bool, command string) error {
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

// RegisterProtocol registers frogmod:// for this user (HKCU\Software\Classes),
// rewriting it whenever the exe has moved.
func RegisterProtocol(exe string) error {
	base := `Software\Classes\` + links.Scheme
	values := []struct{ path, name, value string }{
		{base, "", "URL:Frogtech Mod Loader"},
		{base, "URL Protocol", ""},
		{base + `\DefaultIcon`, "", fmt.Sprintf(`"%s",0`, exe)},
		{base + `\shell\open\command`, "", ProtocolCommand(exe)},
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

func GetRunAtStartup() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runKeyValue)
	return err == nil
}

func ShowError(title, msg string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

func AskYesNo(title, msg string) bool {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	r, _ := windows.MessageBox(0, m, t, windows.MB_YESNO|windows.MB_ICONINFORMATION)
	return r == 6 // IDYES
}
