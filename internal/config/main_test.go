package config

import (
	"os"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.Run(m))
}

func TestTestsUseAThrowawayHome(t *testing.T) {
	testenv.Check(t)
}
