package main

// Self-update from GitHub Releases. The app checks the latest release, and
// when the viewer clicks Update it downloads WarTable.exe plus SHA256SUMS.txt,
// checks the hash, swaps the running exe and restarts.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const updateRepo = "ahoule710/war-table"

var githubAPI = "https://api.github.com"

// testUpdateHost lets tests serve releases from a local server. Empty in real builds.
var testUpdateHost string

type releaseInfo struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
	URL     string `json:"url"`
	exeURL  string
	sumURL  string
}

var (
	updMu        sync.Mutex
	updLatest    *releaseInfo
	updCheckedAt time.Time
	updErr       string
	updBusy      string // "", "downloading", "restarting"
)

var allowedDownloadHosts = map[string]bool{
	"github.com":                            true,
	"api.github.com":                        true,
	"objects.githubusercontent.com":         true,
	"release-assets.githubusercontent.com":  true,
	"github-releases.githubusercontent.com": true,
}

func hostAllowed(u *url.URL) bool {
	if testUpdateHost != "" && u.Host == testUpdateHost {
		return true
	}
	return u.Scheme == "https" && allowedDownloadHosts[u.Hostname()]
}

var updClient = &http.Client{
	Timeout: 3 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if !hostAllowed(req.URL) {
			return fmt.Errorf("refusing redirect to %s", req.URL.Host)
		}
		return nil
	},
}

// newerThan reports whether version a is newer than b ("1.10.0" > "1.9.2").
func newerThan(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.SplitN(pa[i], "-", 2)[0])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.SplitN(pb[i], "-", 2)[0])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func checkForUpdate() {
	req, _ := http.NewRequest("GET", githubAPI+"/repos/"+updateRepo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "WarTable/"+appVersion)
	resp, err := updClient.Do(req)
	updMu.Lock()
	defer updMu.Unlock()
	updCheckedAt = time.Now()
	if err != nil {
		updErr = "Couldn't reach GitHub."
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		updErr = ""
		updLatest = nil // no releases yet
		return
	}
	if resp.StatusCode != 200 {
		updErr = fmt.Sprintf("GitHub answered %d.", resp.StatusCode)
		return
	}
	var r struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&r); err != nil {
		updErr = "Couldn't read the release info."
		return
	}
	info := &releaseInfo{Version: strings.TrimPrefix(r.TagName, "v"), Notes: r.Body, URL: r.HTMLURL}
	for _, a := range r.Assets {
		switch a.Name {
		case "WarTable.exe":
			info.exeURL = a.URL
		case "SHA256SUMS.txt":
			info.sumURL = a.URL
		}
	}
	updLatest, updErr = info, ""
	log.Printf("update check: latest %s, running %s", info.Version, appVersion)
}

func startUpdateChecks(a *App) {
	go func() {
		time.Sleep(8 * time.Second)
		for {
			checkForUpdate()
			select {
			case <-a.quit:
				return
			case <-time.After(6 * time.Hour):
			}
		}
	}()
}

func updateStatus() map[string]any {
	updMu.Lock()
	defer updMu.Unlock()
	out := map[string]any{"current": appVersion, "busy": updBusy, "error": updErr}
	if !updCheckedAt.IsZero() {
		out["checkedAt"] = updCheckedAt.Unix()
	}
	if updLatest != nil {
		out["latest"] = updLatest.Version
		out["notes"] = updLatest.Notes
		out["url"] = updLatest.URL
		out["available"] = newerThan(updLatest.Version, appVersion) && updLatest.exeURL != "" && updLatest.sumURL != ""
	}
	return out
}

func download(u string, limit int64) ([]byte, error) {
	pu, err := url.Parse(u)
	if err != nil || !hostAllowed(pu) {
		return nil, fmt.Errorf("unexpected download address")
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "WarTable/"+appVersion)
	resp, err := updClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download failed (%d)", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func expectedHash(sums []byte, name string) string {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// installUpdate downloads, verifies and swaps in the new exe, then restarts.
func installUpdate(a *App) error {
	updMu.Lock()
	info := updLatest
	if updBusy != "" {
		updMu.Unlock()
		return errors.New("an update is already running")
	}
	if info == nil || !newerThan(info.Version, appVersion) || info.exeURL == "" || info.sumURL == "" {
		updMu.Unlock()
		return errors.New("no update available")
	}
	updBusy = "downloading"
	updMu.Unlock()
	fail := func(err error) error {
		updMu.Lock()
		updBusy = ""
		updMu.Unlock()
		log.Printf("update failed: %v", err)
		return err
	}

	sums, err := download(info.sumURL, 64<<10)
	if err != nil {
		return fail(fmt.Errorf("couldn't get the checksum file: %v", err))
	}
	want := expectedHash(sums, "WarTable.exe")
	if want == "" {
		return fail(errors.New("the release has no checksum for WarTable.exe"))
	}
	exe, err := download(info.exeURL, 64<<20)
	if err != nil {
		return fail(fmt.Errorf("couldn't download the update: %v", err))
	}
	got := sha256.Sum256(exe)
	if hex.EncodeToString(got[:]) != want {
		return fail(errors.New("the download didn't match its checksum, so it wasn't installed"))
	}

	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	self, _ = filepath.EvalSymlinks(self)
	newPath, oldPath := self+".new", self+".old"
	if err := os.WriteFile(newPath, exe, 0755); err != nil {
		return fail(fmt.Errorf("couldn't write next to the app (%v). Move WarTable.exe to a folder you own, like Documents", err))
	}
	_ = os.Remove(oldPath)
	// Windows lets a running exe be renamed, not overwritten.
	if err := os.Rename(self, oldPath); err != nil {
		_ = os.Remove(newPath)
		return fail(err)
	}
	if err := os.Rename(newPath, self); err != nil {
		_ = os.Rename(oldPath, self)
		return fail(err)
	}
	log.Printf("updated %s -> %s; restarting", appVersion, info.Version)
	updMu.Lock()
	updBusy = "restarting"
	updMu.Unlock()

	go func() {
		time.Sleep(400 * time.Millisecond) // let the page get its answer
		a.restartWith(self)
	}()
	return nil
}

// cleanupOldExe removes the previous version left behind by an update.
func cleanupOldExe() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	old := self + ".old"
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(old); os.IsNotExist(err) {
			return
		}
		if os.Remove(old) == nil {
			return
		}
		time.Sleep(time.Second)
	}
}

func startProcess(path string, args ...string) error {
	return exec.Command(path, args...).Start()
}
