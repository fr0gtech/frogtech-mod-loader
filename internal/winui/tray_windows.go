//go:build windows

package winui

import (
	"log"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The tray icon, written directly against Shell_NotifyIcon (no third-party
// library) so it can also show notifications ("balloons", shown as Windows
// toasts) and react to clicks on them. Left click opens the window, right
// click shows Open / Check for updates / Quit.

func init() {
	// The tray's window and message loop live on the main thread (RunUI).
	runtime.LockOSThread()
}

var (
	shell32                = windows.NewLazySystemDLL("shell32.dll")
	pShellNotifyIcon       = shell32.NewProc("Shell_NotifyIconW")
	pCreatePopupMenu       = user32.NewProc("CreatePopupMenu")
	pAppendMenuW           = user32.NewProc("AppendMenuW")
	pDestroyMenu           = user32.NewProc("DestroyMenu")
	pRegisterWindowMessage = user32.NewProc("RegisterWindowMessageW")
	pSetMenuDefaultItem    = user32.NewProc("SetMenuDefaultItem")
)

const (
	nimAdd           = 0
	nimModify        = 1
	nimDelete        = 2
	nimSetVersion    = 4
	nifMessage       = 0x01
	nifIcon          = 0x02
	nifTip           = 0x04
	nifInfo          = 0x10
	nifShowTip       = 0x80
	niifInfo         = 0x01
	niifRespectQuiet = 0x80
	notifyIconVer4   = 4
	wmTrayCallback   = 0x8000 + 10
	ninSelect        = 0x0400
	ninKeySelect     = 0x0401
	ninBalloonClick  = 0x0405
	wmContextMenu    = 0x007B
	wmLButtonUp      = 0x0202
	wmRButtonUp      = 0x0205
	mfString         = 0x0000
	mfSeparator      = 0x0800
	mfChecked        = 0x0008
	trayOpen         = 1
	trayCheck        = 2
	trayQuit         = 3
	trayMods         = 4
	wmNull           = 0x0000
	wmTrayClose      = 0x8000 + 11
	wmTrayNotify     = 0x8000 + 12
	smCxSmIcon       = 49
)

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32 // also uTimeout
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     uintptr
}

var tray struct {
	mu      sync.Mutex
	app     Host
	hwnd    uintptr
	nid     notifyIconData
	nav     string // where a click on the last notification leads
	restart uint32 // "TaskbarCreated": Explorer restarted, add the icon again
	pending []func()
}

func setUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = append(u[:len(dst)-1], 0)
	}
	copy(dst, u)
}

// RunUI owns the tray icon and blocks until the app quits.
func RunUI(a Host) {
	tray.mu.Lock()
	tray.app = a
	tray.mu.Unlock()
	var hinst windows.Handle
	windows.GetModuleHandleEx(0, nil, &hinst)
	cls := windows.StringToUTF16Ptr("FrogLoaderTray")
	wc := wndClassExW{WndProc: windows.NewCallback(trayProc), Instance: hinst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, uintptr(hinst), 0)
	if hwnd == 0 {
		log.Printf("tray: CreateWindowEx: %v", err)
		<-a.Done()
		return
	}
	r, _, _ := pRegisterWindowMessage.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("TaskbarCreated"))))
	tray.mu.Lock()
	tray.hwnd, tray.restart = hwnd, uint32(r)
	tray.mu.Unlock()
	addTrayIcon(hwnd, hinst)
	pumpMessages()
}

func addTrayIcon(hwnd uintptr, hinst windows.Handle) {
	size, _, _ := pGetSystemMetrics.Call(smCxSmIcon)
	icon, _, _ := pLoadImageW.Call(uintptr(hinst), iconResourceID, imageIcon, size, size, lrShared)
	tray.mu.Lock()
	defer tray.mu.Unlock()
	n := &tray.nid
	*n = notifyIconData{Wnd: hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip | nifShowTip, CallbackMessage: wmTrayCallback, Icon: icon}
	n.Size = uint32(unsafe.Sizeof(*n))
	setUTF16(n.Tip[:], "Frogtech Mod Loader")
	if r, _, err := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(n))); r == 0 {
		log.Printf("tray: Shell_NotifyIcon: %v", err)
	}
	n.Version = notifyIconVer4
	pShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(n)))
}

func trayProc(hwnd, msg, wp, lp uintptr) uintptr {
	tray.mu.Lock()
	a, restart := tray.app, tray.restart
	tray.mu.Unlock()
	switch {
	case msg == wmTrayCallback:
		switch uint32(lp & 0xffff) { // NOTIFYICON_VERSION_4: the event is in LOWORD(lParam)
		case ninSelect, ninKeySelect, wmLButtonUp:
			go a.ShowWindow()
		case wmContextMenu, wmRButtonUp:
			showTrayMenu(hwnd, a)
		case ninBalloonClick:
			tray.mu.Lock()
			nav := tray.nav
			tray.mu.Unlock()
			go a.ShowWindowAt(nav)
		}
		return 0
	case msg == wmTrayNotify:
		tray.mu.Lock()
		jobs := tray.pending
		tray.pending = nil
		tray.mu.Unlock()
		for _, j := range jobs {
			j()
		}
		return 0
	case msg == wmTrayClose:
		tray.mu.Lock()
		pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&tray.nid)))
		tray.mu.Unlock()
		pDestroyWindow.Call(hwnd)
		return 0
	case msg == wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	case restart != 0 && uint32(msg) == restart:
		var hinst windows.Handle
		windows.GetModuleHandleEx(0, nil, &hinst)
		addTrayIcon(hwnd, hinst)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func showTrayMenu(hwnd uintptr, a Host) {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	add := func(id uintptr, label string) {
		pAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(label))))
	}
	add(trayOpen, "Open")
	modsOn := a.ModsOn()
	flags := uintptr(mfString)
	if modsOn {
		flags |= mfChecked
	}
	pAppendMenuW.Call(menu, flags, trayMods, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Mods on"))))
	add(trayCheck, "Check for updates")
	pAppendMenuW.Call(menu, mfSeparator, 0, 0)
	add(trayQuit, "Quit")
	pSetMenuDefaultItem.Call(menu, trayOpen, 0)
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd) // so the menu closes when clicking elsewhere
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	pPostMessageW.Call(hwnd, wmNull, 0, 0)
	switch cmd {
	case trayOpen:
		go a.ShowWindow()
	case trayMods:
		go a.SetModsOnFromTray(!modsOn)
	case trayCheck:
		log.Printf("update check requested from tray")
		go a.CheckFromTray()
	case trayQuit:
		go a.Quit()
	}
}

// Notify shows a Windows notification from the tray icon. nav says where a
// click on it leads ("updates" or ""). Safe from any goroutine.
func Notify(title, msg, nav string) { notify(title, msg, nav, niifInfo|niifRespectQuiet) }

// NotifyAlert is a notification that can't wait: shown in quiet hours too, with
// its sound (Windows' Do Not Disturb still holds it back).
func NotifyAlert(title, msg string) { notify(title, msg, "", niifInfo) }

func notify(title, msg, nav string, flags uint32) {
	log.Printf("notification: %s: %s", title, msg)
	tray.mu.Lock()
	hwnd := tray.hwnd
	if hwnd == 0 {
		tray.mu.Unlock()
		return
	}
	tray.pending = append(tray.pending, func() {
		tray.mu.Lock()
		defer tray.mu.Unlock()
		tray.nav = nav
		n := tray.nid
		n.Flags = nifInfo
		n.InfoFlags = flags
		setUTF16(n.InfoTitle[:], title)
		setUTF16(n.Info[:], msg)
		pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&n)))
	})
	tray.mu.Unlock()
	pPostMessageW.Call(hwnd, wmTrayNotify, 0, 0)
}

func closeTray() {
	tray.mu.Lock()
	hwnd := tray.hwnd
	tray.mu.Unlock()
	if hwnd != 0 {
		pPostMessageW.Call(hwnd, wmTrayClose, 0, 0)
	}
}
