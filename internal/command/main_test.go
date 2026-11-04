package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/config"
	"github.com/verygoodsoftwarellc/flare-cli/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m))
}

// Tests here run real commands, including auth logout, so they must never see
// the developer's config, token or server.
func TestTestsUseAThrowawayHome(t *testing.T) {
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Clean(os.TempDir())) {
		t.Fatalf("config path %s is outside the temp directory", path)
	}
	for _, name := range []string{"FLARE_TOKEN", "FLARE_API_URL"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s is set to the developer's value", name)
		}
	}
}
