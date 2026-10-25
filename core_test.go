package main

import (
	"testing"
	"time"
)

const sampleSV = `
WarTableDB = {
	["chars"] = {
		["Brakkar-Area52"] = {
			["name"] = "Brakkar",
			["realm"] = "Area 52",
			["ilvl"] = 711.4000244140625,
			["note"] = "quote \"x\" and \\ slash",
			["runs"] = {
			},
			["vault"] = {
				{
					["progress"] = 2,
					["threshold"] = 1,
				}, -- [1]
				{
					["progress"] = 2,
					["threshold"] = 4,
				}, -- [2]
			},
		},
	},
	["version"] = 1,
}
`

func TestParseSavedVariables(t *testing.T) {
	vars, err := ParseSavedVariables(sampleSV)
	if err != nil {
		t.Fatal(err)
	}
	db := vars["WarTableDB"].(map[string]any)
	c := db["chars"].(map[string]any)["Brakkar-Area52"].(map[string]any)
	if c["note"] != `quote "x" and \ slash` {
		t.Errorf("string escapes: got %q", c["note"])
	}
	if v, ok := c["vault"].([]any); !ok || len(v) != 2 {
		t.Errorf("vault should be a 2-item list, got %#v", c["vault"])
	}
	normalizeLists(c)
	if r, ok := c["runs"].([]any); !ok || len(r) != 0 {
		t.Errorf("empty runs should become an empty list, got %#v", c["runs"])
	}
}

func TestCharIDMergesRealmSpellings(t *testing.T) {
	a := charID("Brakkar-Area52", map[string]any{"name": "Brakkar", "realm": "Area 52"})
	b := charID("Brakkar-Area 52", map[string]any{"name": "Brakkar"})
	if a != b || a != "brakkar-area52" {
		t.Errorf("ids differ: %q vs %q", a, b)
	}
}

func TestMergeKeepsRealStatsOverBlanks(t *testing.T) {
	future := float64(time.Now().Add(48 * time.Hour).Unix())
	good := map[string]any{"ilvl": 711.4, "vault": []any{map[string]any{"progress": 2.0}}, "resetAt": future, "updated": 1.0}
	blank := map[string]any{"ilvl": 0.0, "vault": []any{}, "spec": "Protection", "updated": 2.0}
	m := mergeChar(good, blank)
	if m["ilvl"] != 711.4 || len(m["vault"].([]any)) != 1 || m["spec"] != "Protection" {
		t.Errorf("bad merge: %#v", m)
	}
	past := float64(time.Now().Add(-time.Hour).Unix())
	lastWeek := map[string]any{"vault": []any{1.0}, "resetAt": past, "rating": 2800.0}
	m = mergeChar(lastWeek, map[string]any{"updated": 3.0})
	if _, ok := m["vault"]; ok {
		t.Errorf("last week's vault should be dropped: %#v", m)
	}
	if m["rating"] != 2800.0 {
		t.Errorf("rating should carry over: %#v", m)
	}
}

func TestNewerThan(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.1", "1.2.0", true}, {"1.10.0", "1.9.9", true}, {"v2.0.0", "1.99.99", true},
		{"1.2.0", "1.2.0", false}, {"1.1.9", "1.2.0", false}, {"1.2", "1.2.0", false},
	}
	for _, c := range cases {
		if got := newerThan(c.a, c.b); got != c.want {
			t.Errorf("newerThan(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestExpectedHash(t *testing.T) {
	sums := []byte("abc123  WarTable-v1.2.0.zip\nDEF456 *WarTable.exe\n")
	if h := expectedHash(sums, "WarTable.exe"); h != "def456" {
		t.Errorf("got %q", h)
	}
	if h := expectedHash(sums, "missing.exe"); h != "" {
		t.Errorf("got %q", h)
	}
}
