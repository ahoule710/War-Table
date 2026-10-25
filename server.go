package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// guard rejects requests from anything but our own window: the Host must be
// our loopback address (blocks DNS rebinding) and the per-launch token must match.
func guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != fmt.Sprintf("127.0.0.1:%d", app.port) && host != fmt.Sprintf("localhost:%d", app.port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Header.Get("X-WT-Token") != app.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/focus", handleFocus)
	mux.HandleFunc("/api/state", guard(handleState))
	mux.HandleFunc("/api/local", guard(handleLocal))
	mux.HandleFunc("/api/settings", guard(handleSettings))
	mux.HandleFunc("/api/addon/install", guard(handleInstall))
	mux.HandleFunc("/api/addon/open", guard(handleOpenAddon))
	mux.HandleFunc("/api/open", guard(handleOpen))
	mux.HandleFunc("/api/chat", guard(handleChat))
	mux.HandleFunc("/api/key/test", guard(handleKeyTest))
	mux.HandleFunc("/api/quit", guard(handleQuit))
	mux.HandleFunc("/api/beat", guard(handleBeat))
	mux.HandleFunc("/api/update/check", guard(handleUpdateCheck))
	mux.HandleFunc("/api/update/install", guard(handleUpdateInstall))
	mux.HandleFunc("/api/monitors", guard(handleMonitors))
	mux.HandleFunc("/api/monitor/move", guard(handleMoveMonitor))
	mux.HandleFunc("/api/bye", handleBye)
	return mux
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("t") != app.token {
		http.Error(w, "Open War Table from WarTable.exe.", http.StatusForbidden)
		return
	}
	b, _ := uiFS.ReadFile("ui/index.html")
	html := strings.Replace(string(b), "__WT_TOKEN__", app.token, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = io.WriteString(w, html)
}

func handleFocus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if windowAlive() {
		showWindow()
	} else if app.openWindow != nil {
		heartbeat()
		app.openWindow()
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func handleState(w http.ResponseWriter, r *http.Request) {
	retail := detectWow()
	chars, svTime, parseErr := loadAddonChars(retail)
	local := map[string]any{}
	if b, err := os.ReadFile(filepath.Join(app.dir, "state.json")); err == nil {
		_ = json.Unmarshal(b, &local)
	}
	app.mu.Lock()
	s := app.settings
	app.mu.Unlock()
	svStamp := int64(0)
	if !svTime.IsZero() {
		svStamp = svTime.Unix()
	}
	writeJSON(w, 200, map[string]any{
		"version": appVersion,
		"now":     time.Now().Unix(),
		"settings": map[string]any{
			"hasKey":    s.APIKeyEnc != "",
			"model":     s.Model,
			"fastModel": s.FastModel,
			"webSearch": s.WebSearch,
			"wowPath":   s.WowPath,
			"opacity":   s.Opacity,
			"onTop":     s.OnTop,
		},
		"wow": map[string]any{
			"retail":           retail,
			"found":            retail != "",
			"addonInstalled":   addonInstalledVersion(retail),
			"addonBundled":     bundledAddonVersion(),
			"savedVariablesAt": svStamp,
			"savedVariables":   len(findSavedVariables(retail)),
			"accountsFound":    dirExists(filepath.Join(retail, "WTF", "Account")),
			"parseError":       parseErr,
		},
		"chars":  chars,
		"local":  local,
		"update": updateStatus(),
	})
}

func handleLocal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil || !json.Valid(b) {
		writeJSON(w, 400, map[string]any{"error": "That save didn't look right."})
		return
	}
	if err := writeFileAtomic(filepath.Join(app.dir, "state.json"), b); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

type settingsPatch struct {
	APIKey    *string `json:"apiKey"`
	Model     *string `json:"model"`
	FastModel *string `json:"fastModel"`
	WebSearch *bool   `json:"webSearch"`
	WowPath   *string `json:"wowPath"`
	Opacity   *int    `json:"opacity"`
	OnTop     *bool   `json:"onTop"`
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var p settingsPatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&p); err != nil {
		writeJSON(w, 400, map[string]any{"error": "Bad settings."})
		return
	}
	if p.APIKey != nil {
		key := strings.TrimSpace(*p.APIKey)
		if key != "" && !strings.HasPrefix(key, "sk-ant-") {
			writeJSON(w, 400, map[string]any{"error": "That doesn't look like an Anthropic API key. It should start with sk-ant-."})
			return
		}
		if err := app.setAPIKey(key); err != nil {
			writeJSON(w, 500, map[string]any{"error": "Couldn't store the key securely: " + err.Error()})
			return
		}
	}
	app.mu.Lock()
	if p.Model != nil && strings.TrimSpace(*p.Model) != "" {
		app.settings.Model = strings.TrimSpace(*p.Model)
	}
	if p.FastModel != nil && strings.TrimSpace(*p.FastModel) != "" {
		app.settings.FastModel = strings.TrimSpace(*p.FastModel)
	}
	if p.WebSearch != nil {
		app.settings.WebSearch = *p.WebSearch
	}
	if p.WowPath != nil {
		app.settings.WowPath = strings.TrimSpace(*p.WowPath)
	}
	if p.Opacity != nil {
		o := *p.Opacity
		if o < 40 {
			o = 40
		}
		if o > 100 {
			o = 100
		}
		app.settings.Opacity = o
	}
	if p.OnTop != nil {
		app.settings.OnTop = *p.OnTop
	}
	app.saveSettingsLocked()
	app.mu.Unlock()
	applyWindowNow()
	if p.WowPath != nil && *p.WowPath != "" && normalizeRetail(*p.WowPath) == "" {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": "Saved, but I couldn't find a World of Warcraft _retail_ folder there."})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	retail := detectWow()
	if retail == "" {
		writeJSON(w, 400, map[string]any{"error": "I couldn't find World of Warcraft. Set your WoW folder in Settings first."})
		return
	}
	dest, err := installAddon(retail)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": "Couldn't write to the AddOns folder (" + err.Error() + "). Use Open addon folder and copy WarTableCompanion into Interface\\AddOns yourself."})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": dest})
}

func handleOpenAddon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	p := filepath.Join(app.dir, "addon")
	_ = openFolder(p)
	writeJSON(w, 200, map[string]any{"ok": true, "path": p})
}

func handleOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&body)
	if !strings.HasPrefix(body.URL, "https://") {
		writeJSON(w, 400, map[string]any{"error": "Only https links can be opened."})
		return
	}
	_ = openExternal(body.URL)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		app.stop("quit from settings")
	}()
}

func handleBeat(w http.ResponseWriter, r *http.Request) {
	heartbeat()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleBye takes the token in the query because sendBeacon can't set headers.
func handleBye(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Query().Get("t") != app.token ||
		(r.Host != fmt.Sprintf("127.0.0.1:%d", app.port) && r.Host != fmt.Sprintf("localhost:%d", app.port)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	goodbye()
	w.WriteHeader(204)
}

type MonitorInfo struct {
	Index   int  `json:"index"`
	Primary bool `json:"primary"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Current bool `json:"current"`
}

func handleMonitors(w http.ResponseWriter, r *http.Request) {
	m := listMonitors()
	if m == nil {
		m = []MonitorInfo{}
	}
	writeJSON(w, 200, map[string]any{"monitors": m})
}

func handleMoveMonitor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"error": "Bad request."})
		return
	}
	if err := moveToMonitor(body.Index); err != nil {
		writeJSON(w, 400, map[string]any{"error": "Couldn't move: " + err.Error() + "."})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	checkForUpdate()
	writeJSON(w, 200, updateStatus())
}

func handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := installUpdate(app); err != nil {
		writeJSON(w, 400, map[string]any{"error": "Update didn't install: " + err.Error() + "."})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
