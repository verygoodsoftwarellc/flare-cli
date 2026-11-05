package command

import (
	"fmt"
	"os"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/verygoodsoftwarellc/flare-cli/internal/testenv"
)

// Tests here run real commands, including auth logout. Times print in the
// local zone, so pin it once here: setting time.Local per test races with
// test servers' goroutines.
func TestMain(m *testing.M) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	time.Local = zone
	os.Exit(testenv.Run(m))
}

func TestTestsUseAThrowawayHome(t *testing.T) {
	testenv.Check(t)
}
