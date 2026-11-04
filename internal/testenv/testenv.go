// Package testenv keeps test runs away from the developer's own Flare
// credentials and config.
package testenv

import (
	"fmt"
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

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
	os.Unsetenv("FLARE_TOKEN")
	os.Unsetenv("FLARE_API_URL")
	keyring.MockInit()
	return m.Run()
}
