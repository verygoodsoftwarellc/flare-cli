package command

import (
	"os"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/testenv"
)

// Tests here run real commands, including auth logout.
func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m))
}

func TestTestsUseAThrowawayHome(t *testing.T) {
	testenv.Check(t)
}
