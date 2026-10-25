//go:build !windows

package main

// Stand-ins so the app can be built and tested on non-Windows machines.

import (
	"os"
	"os/exec"
	"strings"
)

func platformInit()                            {}
func protectSecret(b []byte) ([]byte, error)   { return b, nil }
func unprotectSecret(b []byte) ([]byte, error) { return b, nil }
func registryWowPath() string                  { return "" }
func findBrowser() string                      { return "" }
func openExternal(url string) error            { return exec.Command("xdg-open", url).Start() }
func openFolder(p string) error                { return exec.Command("xdg-open", p).Start() }
func startWindowMonitor(a *App)                {}
func startHotkey(a *App)                       {}
func showWindow()                              {}
func applyWindowNow()                          {}
func windowAlive() bool                        { return false }
func closeWindow()                             {}
func removeTray()                              {}
func overlayHidden() bool                      { return false }
func listMonitors() []MonitorInfo              { return nil }
func moveToMonitor(i int) error                { return nil }

// testToken lets tests use a known token. Not present in the Windows build.
func testToken() string { return os.Getenv("WARTABLE_TEST_TOKEN") }

func init() {
	if u := os.Getenv("WARTABLE_TEST_API"); u != "" {
		anthropicURL = u
	}
	if u := os.Getenv("WARTABLE_TEST_GITHUB"); u != "" {
		githubAPI = u
		testUpdateHost = strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	}
}
