package main

import (
	"strconv"
	"strings"
)

// The loader window has no native title bar or frame (window_windows.go). The
// page draws its own bar and asks the native side to act through short
// WebView2 messages ("frog:drag", "frog:resize:13", ...). The pieces that
// don't need Windows live here so they can be tested anywhere.

// WM_NCHITTEST results.
const (
	htClient      = 1
	htCaption     = 2
	htLeft        = 10
	htRight       = 11
	htTop         = 12
	htTopLeft     = 13
	htTopRight    = 14
	htBottom      = 15
	htBottomLeft  = 16
	htBottomRight = 17
)

// hitTest says which part of a frameless window the point (screen
// coordinates) is on: a resize edge or corner within border pixels of the
// window rectangle, otherwise the client area. A maximized window has no
// resize edges.
func hitTest(x, y, left, top, right, bottom, border int, maximized bool) int {
	if maximized || x < left || x >= right || y < top || y >= bottom {
		return htClient
	}
	onL, onR := x < left+border, x >= right-border
	onT, onB := y < top+border, y >= bottom-border
	switch {
	case onT && onL:
		return htTopLeft
	case onT && onR:
		return htTopRight
	case onB && onL:
		return htBottomLeft
	case onB && onR:
		return htBottomRight
	case onL:
		return htLeft
	case onR:
		return htRight
	case onT:
		return htTop
	case onB:
		return htBottom
	}
	return htClient
}

// winMsg is a parsed window-chrome message from the page.
type winMsg struct {
	Action string // drag, resize, min, max, close, sysmenu, state
	Hit    int    // resize: the edge (htLeft..htBottomRight)
}

// parseWinMsg validates "frog:<action>[:<edge>]"; ok is false for anything else.
func parseWinMsg(s string) (winMsg, bool) {
	rest, found := strings.CutPrefix(s, "frog:")
	if !found {
		return winMsg{}, false
	}
	action, arg, _ := strings.Cut(rest, ":")
	switch action {
	case "drag", "min", "max", "close", "sysmenu", "state":
		if arg != "" {
			return winMsg{}, false
		}
		return winMsg{Action: action}, true
	case "resize":
		n, err := strconv.Atoi(arg)
		if err != nil || n < htLeft || n > htBottomRight {
			return winMsg{}, false
		}
		return winMsg{Action: action, Hit: n}, true
	}
	return winMsg{}, false
}
