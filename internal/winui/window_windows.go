//go:build windows

package winui

import (
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/fr0gtech/frogtech-mod-loader/internal/mods"
	"github.com/fr0gtech/frogtech-mod-loader/internal/platform"
	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// The loader's UI is the embedded page shown in a native Win32 window that
// hosts a WebView2 control (the Edge engine that ships with Windows 10/11).
// The window loads the page from the loader's own 127.0.0.1 server; no URL bar,
// no browser. Closing the window tears the WebView2 down (no Edge processes stay
// around while the loader sits in the tray); "Open" builds a new one.
//
// We use github.com/wailsapp/go-webview2's pure-Go WebView2 loader rather than
// one that embeds WebView2Loader.dll and maps it from memory, which is a
// pattern antivirus heuristics dislike.

const webview2DownloadPage = "https://developer.microsoft.com/microsoft-edge/webview2/"

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	gdi32                     = windows.NewLazySystemDLL("gdi32.dll")
	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pShowWindow               = user32.NewProc("ShowWindow")
	pUpdateWindow             = user32.NewProc("UpdateWindow")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pGetWindowPlacement       = user32.NewProc("GetWindowPlacement")
	pSetWindowPlacement       = user32.NewProc("SetWindowPlacement")
	pLoadImageW               = user32.NewProc("LoadImageW")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pIsIconic                 = user32.NewProc("IsIconic")
	pIsWindow                 = user32.NewProc("IsWindow")
	pGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	pGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	pAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	pCreateSolidBrush         = gdi32.NewProc("CreateSolidBrush")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pIsZoomed                 = user32.NewProc("IsZoomed")
	pReleaseCapture           = user32.NewProc("ReleaseCapture")
	pGetSystemMenu            = user32.NewProc("GetSystemMenu")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pEnableMenuItem           = user32.NewProc("EnableMenuItem")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	pGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	pDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	pDwmExtendFrame           = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

const (
	wsOverlappedWindow = 0x00CF0000
	cwUseDefault       = 0x80000000
	swShowNormal       = 1
	swShowMaximized    = 3
	swShow             = 5
	swRestore          = 9
	wmDestroy          = 0x0002
	wmMove             = 0x0003
	wmSize             = 0x0005
	wmActivate         = 0x0006
	wmClose            = 0x0010
	wmGetMinMaxInfo    = 0x0024
	wmMoving           = 0x0216
	wmAppShow          = 0x8000 + 1
	wmAppQuit          = 0x8000 + 2
	imageIcon          = 1
	lrDefaultSize      = 0x0040
	lrShared           = 0x8000
	swMinimize         = 6
	swMaximize         = 3
	wmNCCalcSize       = 0x0083
	wmNCHitTest        = 0x0084
	wmNCActivate       = 0x0086
	wmNCLButtonDown    = 0x00A1
	wmSysCommand       = 0x0112
	swpFrameChanged    = 0x0020
	swpNoMoveSizeZ     = 0x0001 | 0x0002 | 0x0004 | 0x0010 // NOSIZE|NOMOVE|NOZORDER|NOACTIVATE
	tpmReturnCmd       = 0x0100
	tpmRightButton     = 0x0002
	scSize             = 0xF000
	scMove             = 0xF010
	scMinimize         = 0xF020
	scMaximize         = 0xF030
	scRestore          = 0xF120
	mfEnabled          = 0x0000
	mfGrayed           = 0x0001
	smCxFrame          = 32
	smCyFrame          = 33
	smCxPaddedBorder   = 92
	resizeBorder       = 6 // px (scaled by DPI) for WM_NCHITTEST
	idcArrow           = 32512
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type w32Msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type windowPlacement struct {
	Length  uint32
	Flags   uint32
	ShowCmd uint32
	MinPos  point
	MaxPos  point
	Normal  rect
}

type minMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize point
}

var win struct {
	mu       sync.Mutex
	app      Host
	hwnd     uintptr
	starting bool
	ready    bool // chromium embedded and usable
	quitting bool
	chromium *edge.Chromium
}

var (
	registerOnce sync.Once
	className    = windows.StringToUTF16Ptr("FrogLoaderWindow")
)

func WindowOpen() bool {
	win.mu.Lock()
	defer win.mu.Unlock()
	return win.hwnd != 0 || win.starting
}

// ShowWindow opens the window, or brings the existing one to the front.
func ShowWindow(a Host) {
	win.mu.Lock()
	if win.hwnd != 0 {
		h := win.hwnd
		win.mu.Unlock()
		pPostMessageW.Call(h, wmAppShow, 0, 0)
		return
	}
	if win.starting || win.quitting {
		win.mu.Unlock()
		return
	}
	win.starting, win.app = true, a
	win.mu.Unlock()
	go runWindow(a)
}

// closeWindow closes the window as part of quitting and waits briefly so its
// size gets saved.
func closeWindow() {
	win.mu.Lock()
	win.quitting = true
	h := win.hwnd
	win.mu.Unlock()
	if h == 0 {
		return
	}
	pPostMessageW.Call(h, wmAppQuit, 0, 0)
	for i := 0; i < 40 && WindowOpen(); i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

func webview2Version() string {
	v, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString("")
	if err != nil {
		log.Printf("WebView2 runtime: %v", err)
		return ""
	}
	return v
}

func dpiScale(hwnd uintptr) func(int) int32 {
	dpi := uintptr(96)
	if hwnd != 0 && pGetDpiForWindow.Find() == nil {
		dpi, _, _ = pGetDpiForWindow.Call(hwnd)
	} else if pGetDpiForSystem.Find() == nil {
		dpi, _, _ = pGetDpiForSystem.Call()
	}
	if dpi == 0 {
		dpi = 96
	}
	return func(v int) int32 { return int32(v * int(dpi) / 96) }
}

// runWindow owns one window's lifetime on its own OS thread.
func runWindow(a Host) {
	// Keep this goroutine on one OS thread for the window and its COM objects;
	// it is never unlocked, so the thread is discarded when the window closes.
	runtime.LockOSThread()
	defer func() {
		win.mu.Lock()
		win.hwnd, win.starting, win.ready, win.chromium = 0, false, false, nil
		quitting := win.quitting
		win.mu.Unlock()
		if !quitting && !a.KeepInTray() {
			a.Quit()
		}
	}()

	ver := webview2Version()
	if ver == "" {
		if platform.AskYesNo("Frogtech Mod Loader",
			"Frogtech Mod Loader needs the Microsoft Edge WebView2 Runtime, which is missing on this PC.\n\n"+
				"It is free and comes with Windows 10/11 updates. Open the download page now?\n\n"+webview2DownloadPage) {
			platform.OpenURL(webview2DownloadPage)
		}
		return
	}
	log.Printf("opening window (WebView2 %s)", ver)
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)

	var hinst windows.Handle
	windows.GetModuleHandleEx(0, nil, &hinst)
	registerOnce.Do(func() {
		icon, _, _ := pLoadImageW.Call(uintptr(hinst), iconResourceID, imageIcon, 0, 0, lrDefaultSize|lrShared)
		iconSm, _, _ := pLoadImageW.Call(uintptr(hinst), iconResourceID, imageIcon, 16, 16, lrShared)
		cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
		brush, _, _ := pCreateSolidBrush.Call(0x00100e0e) // ink #0e0e10, the page background: no white flash
		wc := wndClassExW{
			WndProc: windows.NewCallback(wndProc), Instance: hinst, ClassName: className,
			Icon: windows.Handle(icon), IconSm: windows.Handle(iconSm), Cursor: windows.Handle(cursor), Background: windows.Handle(brush),
		}
		wc.Size = uint32(unsafe.Sizeof(wc))
		if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			log.Printf("RegisterClassEx: %v", err)
		}
	})

	scale := dpiScale(0)
	title := windows.StringToUTF16Ptr("Frogtech Mod Loader")
	hwnd, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, uintptr(scale(1100)), uintptr(scale(800)), 0, 0, uintptr(hinst), 0)
	if hwnd == 0 {
		log.Printf("CreateWindowEx: %v", err)
		return
	}
	win.mu.Lock()
	win.hwnd, win.starting = hwnd, false
	win.mu.Unlock()

	if r := a.WindowRect(); r != nil && r.W >= 400 && r.H >= 300 {
		wp := windowPlacement{ShowCmd: swShowNormal, Normal: rect{int32(r.X), int32(r.Y), int32(r.X + r.W), int32(r.Y + r.H)}}
		if r.Maximized {
			wp.ShowCmd = swShowMaximized
		}
		wp.Length = uint32(unsafe.Sizeof(wp))
		pSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	} else {
		pShowWindow.Call(hwnd, swShowNormal)
	}
	setupFrame(hwnd)
	pUpdateWindow.Call(hwnd)
	pSetForegroundWindow.Call(hwnd)

	c := edge.NewChromium()
	c.DataPath = filepath.Join(a.DataDir(), "WebView2")
	c.SetErrorCallback(func(err error) {
		log.Printf("WebView2 error: %v", err)
		platform.ShowError("Frogtech Mod Loader", fmt.Sprintf("The window's web view failed: %v", err))
	})
	win.mu.Lock()
	win.chromium = c
	win.mu.Unlock()
	if !c.Embed(hwnd) {
		log.Printf("WebView2 embed failed")
		pDestroyWindow.Call(hwnd)
		pumpMessages()
		return
	}
	if ok, _, _ := pIsWindow.Call(hwnd); ok == 0 { // closed while loading
		pumpMessages()
		return
	}
	if s, err := c.GetSettings(); err == nil {
		s.PutAreDevToolsEnabled(false)
		s.PutAreDefaultContextMenusEnabled(false)
		s.PutIsStatusBarEnabled(false)
		s.PutAreBrowserAcceleratorKeysEnabled(false) // no F5/Ctrl+P/Ctrl+F browser behaviour
	}
	c.SetBackgroundColour(14, 14, 16, 255)
	c.MessageCallback = func(msg string, _ *edge.ICoreWebView2, _ *edge.ICoreWebView2WebMessageReceivedEventArgs) {
		onPageMessage(hwnd, msg)
	}
	win.mu.Lock()
	win.ready = true
	win.mu.Unlock()
	c.Resize()
	c.Navigate(a.OpenURL())
	pumpMessages()
}

func pumpMessages() {
	var m w32Msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT or error
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func readyChromium() *edge.Chromium {
	win.mu.Lock()
	defer win.mu.Unlock()
	if !win.ready {
		return nil
	}
	return win.chromium
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmSize:
		if c := readyChromium(); c != nil {
			c.Resize()
			pushWindowState(hwnd, c)
		}
	case wmNCCalcSize:
		if wp != 0 { // no native frame: the client area is the whole window
			if z, _, _ := pIsZoomed.Call(hwnd); z != 0 {
				// A maximized window hangs over the screen edge by its frame size; pull it in.
				r := (*rect)(*(*unsafe.Pointer)(unsafe.Pointer(&lp)))
				fx, fy := frameSize(hwnd)
				r.Left += fx
				r.Right -= fx
				r.Top += fy
				r.Bottom -= fy
			}
			return 0
		}
	case wmNCHitTest:
		// The page covers the client area and handles its own drag/resize
		// zones; this covers any part of the window it doesn't.
		var wr rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		z, _, _ := pIsZoomed.Call(hwnd)
		x, y := int(int16(lp&0xffff)), int(int16((lp>>16)&0xffff))
		return uintptr(hitTest(x, y, int(wr.Left), int(wr.Top), int(wr.Right), int(wr.Bottom), int(dpiScale(hwnd)(resizeBorder)), z != 0))
	case wmNCActivate:
		// Keep Windows from drawing the (hidden) native caption on focus changes.
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, ^uintptr(0))
		return r
	case wmMove, wmMoving:
		if c := readyChromium(); c != nil {
			c.NotifyParentWindowPositionChanged()
		}
	case wmActivate:
		if wp&0xffff != 0 {
			if c := readyChromium(); c != nil {
				c.Focus()
			}
		}
	case wmGetMinMaxInfo:
		mmi := (*minMaxInfo)(*(*unsafe.Pointer)(unsafe.Pointer(&lp))) // lParam is a MINMAXINFO*
		s := dpiScale(hwnd)
		mmi.MinTrackSize = point{s(800), s(600)}
		return 0
	case wmClose, wmAppQuit:
		saveWindowRect(hwnd)
		win.mu.Lock()
		c := win.chromium
		win.ready = false
		win.mu.Unlock()
		if c != nil {
			closeController(c)
		}
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	case wmAppShow:
		if r, _, _ := pIsIconic.Call(hwnd); r != 0 {
			pShowWindow.Call(hwnd, swRestore)
		} else {
			pShowWindow.Call(hwnd, swShow)
		}
		pSetForegroundWindow.Call(hwnd)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// closeController calls ICoreWebView2Controller::Close (vtable slot 24: 3
// IUnknown methods + 21 before Close in the published interface), which shuts
// down the WebView2 browser processes for this window.
func closeController(c *edge.Chromium) {
	ctrl := c.GetController()
	if ctrl == nil {
		return
	}
	vt := *(**[25]uintptr)(unsafe.Pointer(ctrl))
	syscall.SyscallN(vt[24], uintptr(unsafe.Pointer(ctrl)))
}

func saveWindowRect(hwnd uintptr) {
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return
	}
	win.mu.Lock()
	a := win.app
	win.mu.Unlock()
	if a == nil {
		return
	}
	n := wp.Normal
	a.SetWindowRect(&mods.WindowRect{X: int(n.Left), Y: int(n.Top), W: int(n.Right - n.Left), H: int(n.Bottom - n.Top),
		Maximized: wp.ShowCmd == swShowMaximized})
}

// AllowForeground lets the running instance (pid) bring its window to the
// front when a second launch asks it to.
func AllowForeground(pid int) {
	if pid > 0 {
		pAllowSetForegroundWindow.Call(uintptr(pid))
	}
}

// setupFrame removes the native caption (see WM_NCCALCSIZE) while keeping the
// overlapped window styles, so Aero Snap, Win+arrows, the taskbar button and
// the system menu keep working. DWM gets the Hideout colours, dark mode,
// rounded corners (Windows 11) and its shadow.
// COLORREF is 0x00BBGGRR.
func setupFrame(hwnd uintptr) {
	if pDwmSetWindowAttribute.Find() == nil {
		set := func(attr uintptr, v uint32) {
			pDwmSetWindowAttribute.Call(hwnd, attr, uintptr(unsafe.Pointer(&v)), 4)
		}
		set(20, 1)          // DWMWA_USE_IMMERSIVE_DARK_MODE
		set(33, 2)          // DWMWA_WINDOW_CORNER_PREFERENCE = DWMWCP_ROUND
		set(34, 0x00312c2c) // DWMWA_BORDER_COLOR = line #2c2c31
		set(35, 0x00100e0e) // DWMWA_CAPTION_COLOR = ink #0e0e10
	}
	if pDwmExtendFrame.Find() == nil {
		margins := [4]int32{0, 0, 1, 0} // a sliver of frame keeps DWM's shadow
		pDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	}
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFrameChanged|swpNoMoveSizeZ)
}

// frameSize is the resize frame thickness Windows adds around a window.
func frameSize(hwnd uintptr) (int32, int32) {
	metric := func(i uintptr) int32 {
		if pGetSystemMetricsForDpi.Find() == nil && pGetDpiForWindow.Find() == nil {
			dpi, _, _ := pGetDpiForWindow.Call(hwnd)
			v, _, _ := pGetSystemMetricsForDpi.Call(i, dpi)
			return int32(v)
		}
		v, _, _ := pGetSystemMetrics.Call(i)
		return int32(v)
	}
	pad := metric(smCxPaddedBorder)
	return metric(smCxFrame) + pad, metric(smCyFrame) + pad
}

func pushWindowState(hwnd uintptr, c *edge.Chromium) {
	state := "normal"
	if z, _, _ := pIsZoomed.Call(hwnd); z != 0 {
		state = "max"
	}
	c.Eval(`window.frogWin && window.frogWin("` + state + `")`)
}

// onPageMessage handles the page's title bar: moving, resizing, the window
// buttons and the system menu. Runs on the window thread.
func onPageMessage(hwnd uintptr, raw string) {
	m, ok := parseWinMsg(raw)
	if !ok {
		return
	}
	switch m.Action {
	case "drag", "resize":
		ht := uintptr(htCaption)
		if m.Action == "resize" {
			if z, _, _ := pIsZoomed.Call(hwnd); z != 0 {
				return
			}
			ht = uintptr(m.Hit)
		}
		// Hand the mouse to Windows' own move/size loop (keeps Aero Snap).
		pReleaseCapture.Call()
		pPostMessageW.Call(hwnd, wmNCLButtonDown, ht, 0)
	case "min":
		pShowWindow.Call(hwnd, swMinimize)
	case "max":
		if z, _, _ := pIsZoomed.Call(hwnd); z != 0 {
			pShowWindow.Call(hwnd, swRestore)
		} else {
			pShowWindow.Call(hwnd, swMaximize)
		}
	case "close":
		pPostMessageW.Call(hwnd, wmClose, 0, 0)
	case "sysmenu":
		showSystemMenu(hwnd)
	case "state":
		if c := readyChromium(); c != nil {
			pushWindowState(hwnd, c)
		}
	}
}

func showSystemMenu(hwnd uintptr) {
	menu, _, _ := pGetSystemMenu.Call(hwnd, 0)
	if menu == 0 {
		return
	}
	z, _, _ := pIsZoomed.Call(hwnd)
	enable := func(cmd uintptr, on bool) {
		flag := uintptr(mfGrayed)
		if on {
			flag = mfEnabled
		}
		pEnableMenuItem.Call(menu, cmd, flag)
	}
	enable(scRestore, z != 0)
	enable(scMove, z == 0)
	enable(scSize, z == 0)
	enable(scMaximize, z == 0)
	enable(scMinimize, true)
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	if cmd != 0 {
		pPostMessageW.Call(hwnd, wmSysCommand, cmd, 0)
	}
}
