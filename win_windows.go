//go:build windows

package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	crypt32  = syscall.NewLazyDLL("crypt32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	pEnumWindows                = user32.NewProc("EnumWindows")
	pGetWindowTextW             = user32.NewProc("GetWindowTextW")
	pGetClassNameW              = user32.NewProc("GetClassNameW")
	pIsWindow                   = user32.NewProc("IsWindow")
	pIsWindowVisible            = user32.NewProc("IsWindowVisible")
	pIsIconic                   = user32.NewProc("IsIconic")
	pSetWindowPos               = user32.NewProc("SetWindowPos")
	pGetWindowRect              = user32.NewProc("GetWindowRect")
	pGetWindowLongPtrW          = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW          = user32.NewProc("SetWindowLongPtrW")
	pSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	pRegisterHotKey             = user32.NewProc("RegisterHotKey")
	pGetMessageW                = user32.NewProc("GetMessageW")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	pGetDpiForWindow            = user32.NewProc("GetDpiForWindow")
	pSystemParametersInfoW      = user32.NewProc("SystemParametersInfoW")
	pEnumDisplayMonitors        = user32.NewProc("EnumDisplayMonitors")
	pGetMonitorInfoW            = user32.NewProc("GetMonitorInfoW")
	pMonitorFromRect            = user32.NewProc("MonitorFromRect")
	pMonitorFromWindow          = user32.NewProc("MonitorFromWindow")
	pLocalFree                  = kernel32.NewProc("LocalFree")
	pCryptProtectData           = crypt32.NewProc("CryptProtectData")
	pCryptUnprotectData         = crypt32.NewProc("CryptUnprotectData")
	pRegGetValueW               = advapi32.NewProc("RegGetValueW")
)

const (
	swpNoSize      = 0x0001
	swpNoMove      = 0x0002
	swpNoActivate  = 0x0010
	swpNoZOrder    = 0x0004
	gwlExStyle     = ^uintptr(19) // -20
	wsExLayered    = 0x00080000
	lwaAlpha       = 0x2
	swHide         = 0
	swShowNoActive = 4
	swRestore      = 9
	wmHotkey       = 0x0312
	modAlt         = 0x0001
	modControl     = 0x0002
	modNoRepeat    = 0x4000
	vkW            = 0x57
	hkeyLocal      = 0x80000002
	rrfRtRegSz     = 0x00000002
	spiGetWorkArea = 0x0030
	cryptUIForbid  = 0x1
)

var hwndTopmost = ^uintptr(0)   // -1
var hwndNoTopmost = ^uintptr(1) // -2

type winRect struct{ Left, Top, Right, Bottom int32 }

func platformInit() {
	if pSetProcessDPIAware.Find() == nil {
		pSetProcessDPIAware.Call()
	}
}

// ---------- DPAPI: the API key is encrypted for this Windows user ----------

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func blobOf(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b *dataBlob) bytes() []byte {
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func protectSecret(plain []byte) ([]byte, error) {
	var out dataBlob
	desc, _ := syscall.UTF16PtrFromString("War Table API key")
	r, _, err := pCryptProtectData.Call(uintptr(unsafe.Pointer(blobOf(plain))), uintptr(unsafe.Pointer(desc)), 0, 0, 0, cryptUIForbid, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer pLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

func unprotectSecret(enc []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := pCryptUnprotectData.Call(uintptr(unsafe.Pointer(blobOf(enc))), 0, 0, 0, 0, cryptUIForbid, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer pLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

// ---------- WoW install path from the registry ----------

func regString(root uintptr, subkey, value string) string {
	sk, _ := syscall.UTF16PtrFromString(subkey)
	vn, _ := syscall.UTF16PtrFromString(value)
	var size uint32
	r, _, _ := pRegGetValueW.Call(root, uintptr(unsafe.Pointer(sk)), uintptr(unsafe.Pointer(vn)), rrfRtRegSz, 0, 0, uintptr(unsafe.Pointer(&size)))
	if r != 0 || size == 0 {
		return ""
	}
	buf := make([]uint16, size/2+1)
	r, _, _ = pRegGetValueW.Call(root, uintptr(unsafe.Pointer(sk)), uintptr(unsafe.Pointer(vn)), rrfRtRegSz, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func registryWowPath() string {
	for _, k := range []string{
		`SOFTWARE\WOW6432Node\Blizzard Entertainment\World of Warcraft`,
		`SOFTWARE\Blizzard Entertainment\World of Warcraft`,
	} {
		if p := regString(hkeyLocal, k, "InstallPath"); p != "" {
			return p
		}
	}
	return ""
}

// ---------- Launching things ----------

func findBrowser() string {
	pf86 := os.Getenv("ProgramFiles(x86)")
	pf := os.Getenv("ProgramFiles")
	local := os.Getenv("LOCALAPPDATA")
	for _, p := range []string{
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(local, `Microsoft\Edge\Application\msedge.exe`),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func openExternal(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func openFolder(p string) error {
	return exec.Command("explorer.exe", p).Start()
}

// ---------- The overlay window ----------

var (
	winMu      sync.Mutex
	ourHwnd    uintptr
	lastAlpha  = -1
	lastOnTop  = -1
	placedOnce bool
	enumFound  uintptr
	enumCB     = syscall.NewCallback(enumProc)
)

func enumProc(hwnd, _ uintptr) uintptr {
	cls := make([]uint16, 64)
	pGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), 64)
	if syscall.UTF16ToString(cls) != "Chrome_WidgetWin_1" {
		return 1
	}
	title := make([]uint16, 256)
	pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 256)
	if strings.HasPrefix(syscall.UTF16ToString(title), "War Table") {
		enumFound = hwnd
		return 0
	}
	return 1
}

func findOurWindow() uintptr {
	enumFound = 0
	pEnumWindows.Call(enumCB, 0)
	return enumFound
}

func windowAlive() bool {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd != 0 {
		if r, _, _ := pIsWindow.Call(ourHwnd); r != 0 {
			return true
		}
	}
	ourHwnd = findOurWindow()
	return ourHwnd != 0
}

func dpiScale(h uintptr) float64 {
	if pGetDpiForWindow.Find() != nil {
		return 1
	}
	d, _, _ := pGetDpiForWindow.Call(h)
	if d == 0 {
		return 1
	}
	return float64(d) / 96
}

func placeWindow(h uintptr) {
	app.mu.Lock()
	saved := app.settings.WindowRect
	app.mu.Unlock()
	if saved != nil && saved.W > 200 && saved.H > 200 {
		r := winRect{int32(saved.X), int32(saved.Y), int32(saved.X + saved.W), int32(saved.Y + saved.H)}
		// Only restore if that spot is still on a connected screen.
		if m, _, _ := pMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), 0); m != 0 {
			pSetWindowPos.Call(h, 0, uintptr(saved.X), uintptr(saved.Y), uintptr(saved.W), uintptr(saved.H), swpNoZOrder|swpNoActivate)
			return
		}
		log.Printf("saved position is off-screen (monitor unplugged?); using main screen")
	}
	var wa winRect
	pSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	s := dpiScale(h)
	w := int32(450 * s)
	ht := int32(880 * s)
	if max := wa.Bottom - wa.Top - int32(40*s); ht > max {
		ht = max
	}
	x := wa.Right - w - int32(24*s)
	y := wa.Top + int32(24*s)
	pSetWindowPos.Call(h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), swpNoZOrder|swpNoActivate)
}

func applyWindow(h uintptr, force bool) {
	app.mu.Lock()
	onTop := app.settings.OnTop
	alpha := app.settings.Opacity
	app.mu.Unlock()

	top := 0
	if onTop {
		top = 1
	}
	// Re-assert topmost every pass: full-screen games sometimes knock it down.
	if onTop {
		pSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	} else if force || lastOnTop != top {
		pSetWindowPos.Call(h, hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	}
	lastOnTop = top

	if force || alpha != lastAlpha {
		ex, _, _ := pGetWindowLongPtrW.Call(h, gwlExStyle)
		if alpha >= 100 {
			if ex&wsExLayered != 0 {
				pSetWindowLongPtrW.Call(h, gwlExStyle, ex&^wsExLayered)
			}
		} else {
			if ex&wsExLayered == 0 {
				pSetWindowLongPtrW.Call(h, gwlExStyle, ex|wsExLayered)
			}
			pSetLayeredWindowAttributes.Call(h, 0, uintptr(alpha*255/100), lwaAlpha)
		}
		lastAlpha = alpha
	}
}

func rememberRect(h uintptr) {
	if r, _, _ := pIsIconic.Call(h); r != 0 {
		return
	}
	if r, _, _ := pIsWindowVisible.Call(h); r == 0 {
		return
	}
	var wr winRect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	cur := &Rect{X: int(wr.Left), Y: int(wr.Top), W: int(wr.Right - wr.Left), H: int(wr.Bottom - wr.Top)}
	if cur.W < 200 || cur.H < 200 {
		return
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	old := app.settings.WindowRect
	if old == nil || *old != *cur {
		app.settings.WindowRect = cur
		app.saveSettingsLocked()
	}
}

func startWindowMonitor(a *App) {
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		var goneSince time.Time
		for {
			select {
			case <-a.quit:
				return
			case <-time.After(1500 * time.Millisecond):
			}
			winMu.Lock()
			if ourHwnd != 0 {
				if r, _, _ := pIsWindow.Call(ourHwnd); r == 0 {
					ourHwnd = 0
				}
			}
			if ourHwnd == 0 {
				ourHwnd = findOurWindow()
				if ourHwnd == 0 && placedOnce {
					if goneSince.IsZero() {
						goneSince = time.Now()
					} else if time.Since(goneSince) > 8*time.Second {
						winMu.Unlock()
						a.stop("window closed")
						return
					}
				}
				if ourHwnd != 0 {
					goneSince = time.Time{}
					log.Printf("found window")
					if !placedOnce {
						placeWindow(ourHwnd)
						placedOnce = true
					}
					applyWindow(ourHwnd, true)
					setWindowIcon(ourHwnd)
				} else if time.Now().After(deadline) && !placedOnce {
					log.Printf("window not found yet; overlay controls will apply once it appears")
					deadline = time.Now().Add(time.Hour)
				}
			} else {
				applyWindow(ourHwnd, false)
				rememberRect(ourHwnd)
				iconTick++
				if iconTick%8 == 0 {
					setWindowIcon(ourHwnd)
				}
			}
			winMu.Unlock()
		}
	}()
}

func applyWindowNow() {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd != 0 {
		applyWindow(ourHwnd, true)
	}
}

func showWindow() {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd == 0 {
		ourHwnd = findOurWindow()
	}
	if ourHwnd != 0 {
		hiddenByHotkey = false
		pShowWindow.Call(ourHwnd, swRestore)
		pSetForegroundWindow.Call(ourHwnd)
		applyWindow(ourHwnd, true)
	}
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

func toggleWindow() {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd == 0 {
		ourHwnd = findOurWindow()
	}
	if ourHwnd == 0 {
		return
	}
	if v, _, _ := pIsWindowVisible.Call(ourHwnd); v != 0 {
		pShowWindow.Call(ourHwnd, swHide)
		hiddenByHotkey = true
	} else {
		pShowWindow.Call(ourHwnd, swShowNoActive)
		hiddenByHotkey = false
		applyWindow(ourHwnd, true)
	}
}

func testToken() string { return "" }

var hiddenByHotkey bool
var iconTick int

func overlayHidden() bool {
	winMu.Lock()
	defer winMu.Unlock()
	return hiddenByHotkey
}

func closeWindow() {
	winMu.Lock()
	defer winMu.Unlock()
	if ourHwnd == 0 {
		ourHwnd = findOurWindow()
	}
	if ourHwnd != 0 {
		pPostMessageW.Call(ourHwnd, 0x0010, 0, 0) // WM_CLOSE
	}
}

// ---------- Monitors ----------

type monitorInfoEx struct {
	cbSize   uint32
	rcMon    winRect
	rcWork   winRect
	dwFlags  uint32
	szDevice [32]uint16
}

type monitor struct {
	handle  uintptr
	work    winRect
	full    winRect
	primary bool
}

var (
	monList []monitor
	monCB   = syscall.NewCallback(func(h, _, _, _ uintptr) uintptr {
		var mi monitorInfoEx
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := pGetMonitorInfoW.Call(h, uintptr(unsafe.Pointer(&mi))); r != 0 {
			monList = append(monList, monitor{handle: h, work: mi.rcWork, full: mi.rcMon, primary: mi.dwFlags&1 != 0})
		}
		return 1
	})
	monMu sync.Mutex
)

func monitors() []monitor {
	monMu.Lock()
	defer monMu.Unlock()
	monList = nil
	pEnumDisplayMonitors.Call(0, 0, monCB, 0)
	out := append([]monitor(nil), monList...)
	// Main screen first, then left to right.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if (!a.primary && b.primary) || (a.primary == b.primary && b.full.Left < a.full.Left) {
				out[j-1], out[j] = b, a
			}
		}
	}
	return out
}

func listMonitors() []MonitorInfo {
	mons := monitors()
	var cur uintptr
	winMu.Lock()
	if ourHwnd != 0 {
		cur, _, _ = pMonitorFromWindow.Call(ourHwnd, 2)
	}
	winMu.Unlock()
	out := make([]MonitorInfo, len(mons))
	for i, m := range mons {
		out[i] = MonitorInfo{Index: i, Primary: m.primary, Width: int(m.full.Right - m.full.Left), Height: int(m.full.Bottom - m.full.Top), Current: m.handle == cur}
	}
	return out
}

func moveToMonitor(i int) error {
	mons := monitors()
	if i < 0 || i >= len(mons) {
		return errors.New("that screen isn't connected")
	}
	winMu.Lock()
	if ourHwnd == 0 {
		ourHwnd = findOurWindow()
	}
	h := ourHwnd
	winMu.Unlock()
	if h == 0 {
		return errors.New("the War Table window isn't open")
	}
	wa := mons[i].work
	var wr winRect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	w, ht := wr.Right-wr.Left, wr.Bottom-wr.Top
	// Hop onto the target screen first so it picks up that screen's scaling.
	pSetWindowPos.Call(h, 0, uintptr(wa.Right-w-24), uintptr(wa.Top+24), uintptr(w), uintptr(ht), swpNoZOrder|swpNoActivate)
	time.Sleep(200 * time.Millisecond)
	s := dpiScale(h)
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&wr)))
	w, ht = wr.Right-wr.Left, wr.Bottom-wr.Top
	if max := wa.Bottom - wa.Top - int32(40*s); ht > max {
		ht = max
	}
	x := wa.Right - w - int32(24*s)
	y := wa.Top + int32(24*s)
	pSetWindowPos.Call(h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), swpNoZOrder|swpNoActivate)
	rememberRect(h)
	return nil
}
