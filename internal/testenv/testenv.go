// Package testenv keeps test runs away from the developer's own Flare
// credentials and config.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

var clearedEnv = []string{"FLARE_TOKEN", "FLARE_API_URL"}

// Run is for TestMain. It points the home and config directories at a
// throwaway one, clears FLARE_TOKEN and FLARE_API_URL, and swaps the OS
// credential store for an in-memory one before running the tests, so a test
// that skips its own setup can't read, replace or delete a real token, or
// send one to a real server.
func Run(m *testing.M) int {
	home, err := os.MkdirTemp("", "flare-cli-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
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
	if !strings.HasPrefix(directory, filepath.Clean(os.TempDir())) {
		t.Fatalf("config directory %s is outside the temp directory", directory)
	}
	for _, name := range clearedEnv {
		if os.Getenv(name) != "" {
			t.Fatalf("%s is set to the developer's value", name)
		}
	}
}
