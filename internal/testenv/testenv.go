// Package testenv keeps test runs away from the developer's own Flare
// credentials and config, and the Claude Code config `setup` edits.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

var clearedEnv = []string{"FLARE_TOKEN", "FLARE_API_URL"}

// home is the throwaway directory Run made, for Check.
var home string

// Run is for TestMain. It points the home and config directories, Claude
// Code's included, at a throwaway one, clears FLARE_TOKEN and FLARE_API_URL,
// and swaps the OS credential store for an in-memory one before running the
// tests, so a test that skips its own setup can't read, replace or delete a
// real token or client config, or send a token to a real server.
func Run(m *testing.M) int {
	var err error
	home, err = os.MkdirTemp("", "flare-cli-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "CLAUDE_CONFIG_DIR"} {
		os.Setenv(name, home)
	}
	for _, name := range clearedEnv {
		os.Unsetenv(name)
	}
	keyring.MockInit()
	return m.Run()
}

// Check fails the test unless Run is in effect, so a package that loses its
// TestMain finds out.
func Check(t *testing.T) {
	t.Helper()
	directory, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if home == "" {
		t.Fatal("testenv.Run is not in effect")
	}
	for _, directory := range []string{directory, os.Getenv("CLAUDE_CONFIG_DIR")} {
		if relative, err := filepath.Rel(home, directory); err != nil || !filepath.IsLocal(relative) {
			t.Fatalf("config directory %q is outside the throwaway home %s", directory, home)
		}
	}
	for _, name := range clearedEnv {
		if os.Getenv(name) != "" {
			t.Fatalf("%s is set to the developer's value", name)
		}
	}
}
