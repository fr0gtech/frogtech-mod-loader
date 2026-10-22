//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
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
func IsGameRunning() bool { return len(gamePIDs()) > 0 }

// gamePIDs lists the process ids of deadlock.exe.
func gamePIDs() map[uint32]bool {
	pids := map[uint32]bool{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return pids
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), "deadlock.exe") {
			pids[pe.ProcessID] = true
		}
	}
	return pids
}

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	pFlashWindowEx  = user32.NewProc("FlashWindowEx")
	pGetWindow      = user32.NewProc("GetWindow")
	enumGameWindows = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		f := &search
		var pid uint32
		windows.GetWindowThreadProcessId(hwnd, &pid)
		if f.pids[pid] && windows.IsWindowVisible(hwnd) {
			if owner, _, _ := pGetWindow.Call(uintptr(hwnd), gwOwner); owner == 0 {
				f.found = hwnd
				return 0 // stop
			}
		}
		return 1
	})
)

const gwOwner = 4

// search is what enumGameWindows looks for and found (searchMu held).
var (
	searchMu sync.Mutex
	search   struct {
		pids  map[uint32]bool
		found windows.HWND
	}
)

// gameWindow is Deadlock's main window, or 0.
func gameWindow() windows.HWND {
	pids := gamePIDs()
	if len(pids) == 0 {
		return 0
	}
	searchMu.Lock()
	defer searchMu.Unlock()
	search.pids, search.found = pids, 0
	windows.EnumWindows(enumGameWindows, nil) // "fails" when the callback stops it
	return search.found
}

type flashWInfo struct {
	Size    uint32
	Hwnd    windows.HWND
	Flags   uint32
	Count   uint32
	Timeout uint32
}

// FlashGameWindow flashes Deadlock's taskbar button until the player switches
// to the game. False when the game window isn't there.
func FlashGameWindow() bool {
	h := gameWindow()
	if h == 0 {
		return false
	}
	const flashAll, flashTimerNoFG = 0x3, 0xC
	fi := flashWInfo{Hwnd: h, Flags: flashAll | flashTimerNoFG}
	fi.Size = uint32(unsafe.Sizeof(fi))
	pFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	return true
}

// GameIsForeground says whether the player is looking at Deadlock right now.
func GameIsForeground() bool {
	h := windows.GetForegroundWindow()
	if h == 0 {
		return false
	}
	var pid uint32
	windows.GetWindowThreadProcessId(h, &pid)
	return pid != 0 && gamePIDs()[pid]
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
