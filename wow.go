package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// normalizeRetail turns any WoW install path into the _retail_ folder, if it can.
func normalizeRetail(p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if strings.EqualFold(filepath.Base(p), "_retail_") && dirExists(p) {
		return p
	}
	if dirExists(filepath.Join(p, "_retail_")) {
		return filepath.Join(p, "_retail_")
	}
	// Someone pointed at Interface or AddOns; walk up.
	cur := p
	for i := 0; i < 4; i++ {
		if strings.EqualFold(filepath.Base(cur), "_retail_") && dirExists(cur) {
			return cur
		}
		cur = filepath.Dir(cur)
	}
	if dirExists(filepath.Join(p, "Interface")) || dirExists(filepath.Join(p, "WTF")) {
		return p
	}
	return ""
}

func detectWow() string {
	app.mu.Lock()
	manual := app.settings.WowPath
	app.mu.Unlock()
	if r := normalizeRetail(manual); r != "" {
		return r
	}
	if r := normalizeRetail(registryWowPath()); r != "" {
		return r
	}
	var candidates []string
	for _, d := range "CDEFGH" {
		root := string(d) + `:\`
		for _, sub := range []string{
			`Program Files (x86)\World of Warcraft`,
			`Program Files\World of Warcraft`,
			`World of Warcraft`,
			`Games\World of Warcraft`,
			`Battle.net\World of Warcraft`,
			`Blizzard\World of Warcraft`,
		} {
			candidates = append(candidates, root+sub)
		}
	}
	for _, c := range candidates {
		if r := normalizeRetail(c); r != "" {
			return r
		}
	}
	return ""
}

type svInfo struct {
	Path  string
	MTime time.Time
}

// findSavedVariables returns every WarTableCompanion.lua under WTF\Account, newest first.
func findSavedVariables(retail string) []svInfo {
	var out []svInfo
	if retail == "" {
		return out
	}
	accounts := filepath.Join(retail, "WTF", "Account")
	entries, err := os.ReadDir(accounts)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(accounts, e.Name(), "SavedVariables", "WarTableCompanion.lua")
		if st, err := os.Stat(p); err == nil {
			out = append(out, svInfo{p, st.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MTime.After(out[j].MTime) })
	return out
}

// loadAddonChars merges character data from every account file.
func loadAddonChars(retail string) (map[string]any, time.Time, string) {
	chars := map[string]any{}
	var newest time.Time
	var parseErr string
	for _, sv := range findSavedVariables(retail) {
		b, err := os.ReadFile(sv.Path)
		if err != nil {
			continue
		}
		vars, err := ParseSavedVariables(string(b))
		if err != nil {
			parseErr = err.Error()
			continue
		}
		db, _ := vars["WarTableDB"].(map[string]any)
		cm, _ := db["chars"].(map[string]any)
		for k, v := range cm {
			cv, ok := v.(map[string]any)
			if !ok {
				continue
			}
			normalizeLists(cv)
			k = charID(k, cv)
			if old, ok := chars[k].(map[string]any); ok {
				if num(old["updated"]) >= num(cv["updated"]) {
					chars[k] = mergeChar(cv, old)
				} else {
					chars[k] = mergeChar(old, cv)
				}
				continue
			}
			chars[k] = cv
		}
		if sv.MTime.After(newest) {
			newest = sv.MTime
		}
	}
	return chars, newest, parseErr
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func addonInstalledVersion(retail string) string {
	if retail == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(retail, "Interface", "AddOns", "WarTableCompanion", "WarTableCompanion.toc"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "## Version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "## Version:"))
		}
	}
	return "unknown"
}

func bundledAddonVersion() string {
	b, _ := addonFS.ReadFile("addon/WarTableCompanion/WarTableCompanion.toc")
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "## Version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "## Version:"))
		}
	}
	return ""
}

func copyAddonTo(dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	return fs.WalkDir(addonFS, "addon/WarTableCompanion", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := addonFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, filepath.Base(p)), b, 0644)
	})
}

func installAddon(retail string) (string, error) {
	dest := filepath.Join(retail, "Interface", "AddOns", "WarTableCompanion")
	return dest, copyAddonTo(dest)
}

// extractAddonCopy keeps a copy next to the app data so it can be installed by hand.
func extractAddonCopy(dir string) {
	_ = copyAddonTo(filepath.Join(dir, "addon", "WarTableCompanion"))
}

var idCleaner = strings.NewReplacer(" ", "", "-", "", "'", "")

// charID gives one stable id per character, however the realm was spelled.
func charID(key string, cv map[string]any) string {
	name, _ := cv["name"].(string)
	realm, _ := cv["realm"].(string)
	if name == "" {
		name = key
		if i := strings.LastIndex(key, "-"); i >= 0 {
			name = key[:i]
		}
	}
	if realm == "" {
		if i := strings.LastIndex(key, "-"); i >= 0 {
			realm = key[i+1:]
		}
	}
	return strings.ToLower(idCleaner.Replace(name)) + "-" + strings.ToLower(idCleaner.Replace(realm))
}

// updateAddonIfOld refreshes an already-installed addon when the app ships a newer one.
func updateAddonIfOld() {
	retail := detectWow()
	installed := addonInstalledVersion(retail)
	if installed == "" || installed == bundledAddonVersion() {
		return
	}
	if dest, err := installAddon(retail); err != nil {
		log.Printf("addon update failed: %v", err)
	} else {
		log.Printf("updated addon %s -> %s at %s", installed, bundledAddonVersion(), dest)
	}
}

// mergeChar combines two snapshots of one character. Newer values win unless
// they're blank (item level 0, empty Vault), which happens when WoW is
// shutting down. Weekly data from a snapshot taken before reset is dropped.
func mergeChar(older, newer map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range older {
		out[k] = v
	}
	if ra := num(older["resetAt"]); ra > 0 && ra <= float64(time.Now().Unix()) {
		for _, k := range []string{"vault", "vaultReady", "runs", "raids", "keystone"} {
			delete(out, k)
		}
	}
	for k, v := range newer {
		if meaningful(v) {
			out[k] = v
		}
	}
	return out
}

func meaningful(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// WoW saves an empty list as {} which reads as an empty map. Make the list
// fields real lists so the UI always gets arrays.
func normalizeLists(cv map[string]any) {
	for _, f := range []string{"vault", "runs", "raids"} {
		switch v := cv[f].(type) {
		case []any:
		case map[string]any:
			list := make([]any, 0, len(v))
			for _, item := range v {
				list = append(list, item)
			}
			cv[f] = list
		case nil:
		default:
			delete(cv, f)
		}
	}
}
