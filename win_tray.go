//go:build windows

package main

// The notification-area (tray) icon, its tooltip and menu, and the
// Ctrl+Alt+W hotkey all live on one hidden window with its own thread.

import (
	"fmt"
	"log"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")

	pShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	pRegisterClassExW       = user32.NewProc("RegisterClassExW")
	pCreateWindowExW        = user32.NewProc("CreateWindowExW")
	pDefWindowProcW         = user32.NewProc("DefWindowProcW")
	pTranslateMessage       = user32.NewProc("TranslateMessage")
	pDispatchMessageW       = user32.NewProc("DispatchMessageW")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pLoadImageW             = user32.NewProc("LoadImageW")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pSetTimer               = user32.NewProc("SetTimer")
	pGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmTray       = 0x8001 // WM_APP + 1
	wmTimer      = 0x0113
	wmLButtonUp  = 0x0202
	wmRButtonUp  = 0x0205
	wmNull       = 0x0000
	wmSetIcon    = 0x0080
	nimAdd       = 0
	nimModify    = 1
	nimDelete    = 2
	nifMessage   = 0x1
	nifIcon      = 0x2
	nifTip       = 0x4
	mfString     = 0x0
	mfSeparator  = 0x800
	mfGrayed     = 0x1
	tpmReturnCmd = 0x100
	tpmRightBtn  = 0x2
	tpmBottom    = 0x20
	imageIcon    = 1
	smCxIcon     = 11
	smCyIcon     = 12
	smCxSmIcon   = 49
	smCySmIcon   = 50

	menuToggle = 1
	menuScreen = 2
	menuQuit   = 3
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         guid
	hBalloonIcon     uintptr
}

var (
	shellApp       *App
	shellHwnd      uintptr
	iconSmall      uintptr
	iconBig        uintptr
	taskbarCreated uintptr
	shellProc      = syscall.NewCallback(shellWndProc)
)

func loadIcons() {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	sx, _, _ := pGetSystemMetrics.Call(smCxSmIcon)
	sy, _, _ := pGetSystemMetrics.Call(smCySmIcon)
	bx, _, _ := pGetSystemMetrics.Call(smCxIcon)
	by, _, _ := pGetSystemMetrics.Call(smCyIcon)
	iconSmall, _, _ = pLoadImageW.Call(hInst, 1, imageIcon, sx, sy, 0)
	iconBig, _, _ = pLoadImageW.Call(hInst, 1, imageIcon, bx, by, 0)
}

// setWindowIcon puts our icon on the War Table window (title bar, Alt-Tab, taskbar).
func setWindowIcon(h uintptr) {
	if h == 0 || iconSmall == 0 {
		return
	}
	pPostMessageW.Call(h, wmSetIcon, 0, iconSmall)
	pPostMessageW.Call(h, wmSetIcon, 1, iconBig)
}

func resetCountdown() string {
	now := time.Now().UTC()
	d := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, time.UTC)
	d = d.AddDate(0, 0, -((int(d.Weekday()) - 2 + 7) % 7))
	if d.After(now) {
		d = d.AddDate(0, 0, -7)
	}
	left := d.AddDate(0, 0, 7).Sub(now)
	days := int(left.Hours()) / 24
	hours := int(left.Hours()) % 24
	mins := int(left.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func trayData(flags uint32) *notifyIconData {
	var nid notifyIconData
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = shellHwnd
	nid.uID = 1
	nid.uFlags = flags
	nid.uCallbackMessage = wmTray
	nid.hIcon = iconSmall
	tip, _ := syscall.UTF16FromString("War Table\nWeekly reset in " + resetCountdown())
	copy(nid.szTip[:len(nid.szTip)-1], tip)
	return &nid
}

func addTray() {
	pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(trayData(nifMessage|nifIcon|nifTip))))
}

func updateTray() {
	pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(trayData(nifTip))))
}

func removeTray() {
	if shellHwnd != 0 {
		pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(trayData(0))))
	}
}

func windowShowing() bool {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd == 0 {
		return false
	}
	v, _, _ := pIsWindowVisible.Call(ourHwnd)
	m, _, _ := pIsIconic.Call(ourHwnd)
	return v != 0 && m == 0
}

func trayClick() {
	if !windowAlive() {
		if shellApp != nil && shellApp.openWindow != nil {
			heartbeat()
			go shellApp.openWindow()
		}
		return
	}
	if windowShowing() {
		hideWindow()
	} else {
		showWindow()
	}
}

func hideWindow() {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd != 0 {
		pShowWindow.Call(ourHwnd, swHide)
		hiddenByHotkey = true
	}
}

func nextScreen() {
	mons := listMonitors()
	if len(mons) < 2 {
		return
	}
	cur := 0
	for _, m := range mons {
		if m.Current {
			cur = m.Index
		}
	}
	go func() { _ = moveToMonitor((cur + 1) % len(mons)) }()
}

func trayMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	label := "Show War Table"
	if windowShowing() {
		label = "Hide War Table"
	}
	add := func(flags uint32, id uintptr, text string) {
		t, _ := syscall.UTF16PtrFromString(text)
		pAppendMenuW.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(t)))
	}
	add(mfString, menuToggle, label+"\tCtrl+Alt+W")
	if len(listMonitors()) > 1 {
		add(mfString, menuScreen, "Move to next screen")
	}
	add(mfSeparator, 0, "")
	add(mfString, menuQuit, "Quit War Table")

	var pt struct{ x, y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(shellHwnd)
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightBtn|tpmBottom, uintptr(pt.x), uintptr(pt.y), 0, shellHwnd, 0)
	pPostMessageW.Call(shellHwnd, wmNull, 0, 0)
	switch cmd {
	case menuToggle:
		trayClick()
	case menuScreen:
		nextScreen()
	case menuQuit:
		if shellApp != nil {
			shellApp.stop("quit from tray")
		}
	}
}

func shellWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTray:
		switch lParam & 0xffff {
		case wmLButtonUp:
			trayClick()
		case wmRButtonUp:
			trayMenu()
		}
		return 0
	case wmHotkey:
		toggleWindow()
		return 0
	case wmTimer:
		updateTray()
		return 0
	}
	if taskbarCreated != 0 && msg == taskbarCreated {
		addTray() // Explorer restarted; put the icon back.
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// startHotkey starts the shell thread: hidden window, tray icon, hotkey.
func startHotkey(a *App) {
	shellApp = a
	go func() {
		runtime.LockOSThread()
		loadIcons()
		hInst, _, _ := pGetModuleHandleW.Call(0)
		cls, _ := syscall.UTF16PtrFromString("WarTableShell")
		wc := wndClassEx{lpfnWndProc: shellProc, hInstance: hInst, lpszClassName: cls, hIcon: iconBig, hIconSm: iconSmall}
		wc.cbSize = uint32(unsafe.Sizeof(wc))
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		title, _ := syscall.UTF16PtrFromString("War Table Shell")
		shellHwnd, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0, 0, 0, hInst, 0)
		if shellHwnd == 0 {
			log.Printf("could not create tray window")
			return
		}
		tc, _ := syscall.UTF16PtrFromString("TaskbarCreated")
		taskbarCreated, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
		addTray()
		pSetTimer.Call(shellHwnd, 1, 30000, 0)
		if r, _, err := pRegisterHotKey.Call(shellHwnd, 1, modControl|modAlt|modNoRepeat, vkW); r == 0 {
			log.Printf("hotkey Ctrl+Alt+W unavailable: %v", err)
		}
		var m winMsg
		for {
			ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(ret) <= 0 {
				return
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
}
