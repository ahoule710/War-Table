package main

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

//go:embed ui/index.html
var uiFS embed.FS

//go:embed addon/WarTableCompanion/*
var addonFS embed.FS

// appVersion is set by the release build (-X main.appVersion=...).
var appVersion = "1.2.0"

const preferredPort = 47818

type Rect struct {
	X, Y, W, H int
}

type Settings struct {
	APIKeyEnc  string `json:"apiKeyEnc,omitempty"`
	Model      string `json:"model"`
	FastModel  string `json:"fastModel"`
	WebSearch  bool   `json:"webSearch"`
	WowPath    string `json:"wowPath,omitempty"`
	Opacity    int    `json:"opacity"`
	OnTop      bool   `json:"onTop"`
	WindowRect *Rect  `json:"windowRect,omitempty"`
}

type App struct {
	mu          sync.Mutex
	dir         string
	settings    Settings
	token       string
	port        int
	quit        chan struct{}
	openWindow  func()
	restartPath string
}

var app *App

func defaultSettings() Settings {
	return Settings{Model: "claude-sonnet-5-5", FastModel: "claude-haiku-5-5", WebSearch: true, Opacity: 100, OnTop: true}
}

func dataDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "WarTable")
}

func (a *App) loadSettings() {
	a.settings = defaultSettings()
	b, err := os.ReadFile(filepath.Join(a.dir, "settings.json"))
	if err == nil {
		_ = json.Unmarshal(b, &a.settings)
	}
	if a.settings.Opacity < 40 || a.settings.Opacity > 100 {
		a.settings.Opacity = 100
	}
	if a.settings.Model == "" {
		a.settings.Model = "claude-sonnet-5-5"
	}
	if a.settings.FastModel == "" {
		a.settings.FastModel = "claude-haiku-5-5"
	}
}

// saveSettingsLocked writes settings; caller holds a.mu.
func (a *App) saveSettingsLocked() {
	b, _ := json.MarshalIndent(a.settings, "", "  ")
	writeFileAtomic(filepath.Join(a.dir, "settings.json"), b)
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *App) apiKey() string {
	a.mu.Lock()
	enc := a.settings.APIKeyEnc
	a.mu.Unlock()
	if enc == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return ""
	}
	plain, err := unprotectSecret(raw)
	if err != nil {
		return ""
	}
	return string(plain)
}

func (a *App) setAPIKey(key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if key == "" {
		a.settings.APIKeyEnc = ""
		a.saveSettingsLocked()
		return nil
	}
	enc, err := protectSecret([]byte(key))
	if err != nil {
		return err
	}
	a.settings.APIKeyEnc = base64.StdEncoding.EncodeToString(enc)
	a.saveSettingsLocked()
	return nil
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	platformInit()
	dir := dataDir()
	_ = os.MkdirAll(dir, 0700)

	logPath := filepath.Join(dir, "log.txt")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 512*1024 {
		_ = os.Rename(logPath, filepath.Join(dir, "log.old.txt"))
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		log.SetOutput(io.MultiWriter(logFile, os.Stderr))
	}
	log.Printf("War Table %s starting", appVersion)

	tok := testToken()
	if tok == "" {
		tok = randomToken()
	}
	app = &App{dir: dir, token: tok, quit: make(chan struct{})}
	app.loadSettings()
	extractAddonCopy(dir)

	justUpdated := false
	for _, a := range os.Args[1:] {
		if a == "--updated" {
			justUpdated = true
		}
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	// Right after an update the old version may still be letting go of the port.
	for i := 0; err != nil && justUpdated && i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	}
	if err != nil {
		// Already running? Ask it to show its window and exit.
		c := http.Client{Timeout: 2 * time.Second}
		if resp, perr := c.Post(fmt.Sprintf("http://127.0.0.1:%d/api/focus", preferredPort), "application/json", nil); perr == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				log.Printf("another instance is running; focused it")
				return
			}
		}
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalf("could not open a local port: %v", err)
		}
	}
	app.port = ln.Addr().(*net.TCPAddr).Port
	go updateAddonIfOld()
	go cleanupOldExe()
	startUpdateChecks(app)
	url := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", app.port, app.token)

	srv := &http.Server{Handler: routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server stopped: %v", err)
		}
	}()

	app.openWindow = func() {
		browser := findBrowser()
		if browser == "" {
			log.Printf("no Chrome or Edge found; opening default browser")
			_ = openExternal(url)
			return
		}
		cmd := exec.Command(browser,
			"--app="+url,
			"--user-data-dir="+filepath.Join(dir, "browser"),
			"--window-size=460,880",
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-sync",
			"--disable-features=Translate",
		)
		if err := cmd.Start(); err != nil {
			log.Printf("could not start %s: %v", browser, err)
			_ = openExternal(url)
			return
		}
		log.Printf("window launched with %s", browser)
		go func() {
			_ = cmd.Wait()
			log.Printf("browser launcher exited (normal; the window keeps running)")
		}()
	}
	app.openWindow()

	startWindowMonitor(app)
	startHotkey(app)
	go watchdog(app)

	<-app.quit
	removeTray()
	closeWindow()
	time.Sleep(300 * time.Millisecond)
	_ = srv.Close()
	if app.restartPath != "" {
		if err := startProcess(app.restartPath, "--updated"); err != nil {
			log.Printf("couldn't restart after update: %v", err)
		}
	}
	log.Printf("War Table closed")
}

// ---------- Lifetime ----------
// The app lives as long as its page does. The page checks in every 15s and
// sends a goodbye when it closes. Edge's launcher process exiting means nothing.

var (
	lifeMu   sync.Mutex
	lastBeat = time.Now()
	byeAt    time.Time
	stopOnce sync.Once
)

func heartbeat() {
	lifeMu.Lock()
	lastBeat = time.Now()
	byeAt = time.Time{}
	lifeMu.Unlock()
}

func goodbye() {
	log.Printf("window said goodbye")
	lifeMu.Lock()
	byeAt = time.Now()
	lifeMu.Unlock()
}

func (a *App) stop(reason string) {
	stopOnce.Do(func() {
		log.Printf("shutting down: %s", reason)
		close(a.quit)
	})
}

func watchdog(a *App) {
	for {
		select {
		case <-a.quit:
			return
		case <-time.After(2 * time.Second):
		}
		lifeMu.Lock()
		lb, ba := lastBeat, byeAt
		lifeMu.Unlock()
		if !ba.IsZero() && time.Since(ba) > 6*time.Second {
			a.stop("window closed")
			return
		}
		if time.Since(lb) > 3*time.Minute && !overlayHidden() {
			a.stop("no window has checked in for 3 minutes")
			return
		}
	}
}

// restartWith quits and starts the given exe (used after an update).
func (a *App) restartWith(path string) {
	a.restartPath = path
	a.stop("restarting into the new version")
}
