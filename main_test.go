package main

import (
	"errors"
	"os"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/testenv"
)

// The MCP helper process keeps the environment its end-to-end test set up:
// a temp home, plus the stub server's URL and token.
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		os.Exit(m.Run())
	}
	os.Exit(testenv.Run(m))
}

func TestTestsUseAThrowawayHome(t *testing.T) {
	testenv.Check(t)
}

func TestFormatErrorUsesInvokedExecutableName(t *testing.T) {
	got := formatError("flare-cli", errors.New("not authenticated"))
	if got != "flare-cli: not authenticated" {
		t.Fatalf("unexpected error %q", got)
	}
}
