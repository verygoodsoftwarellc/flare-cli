package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOpenBrowserReapsExitedHelper(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process-state probe uses Linux /proc")
	}
	directory := t.TempDir()
	pidFile := filepath.Join(directory, "pid")
	helper := filepath.Join(directory, "xdg-open")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%d' $$ > \"$FLARE_BROWSER_PID_FILE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("FLARE_BROWSER_PID_FILE", pidFile)
	if err := OpenBrowser("http://127.0.0.1/"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	pid := 0
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("helper did not record its PID")
	}
	// Reap the baseline's orphaned child after checking, so even red tests clean up.
	defer func() {
		if process, err := os.FindProcess(pid); err == nil {
			_, _ = process.Wait()
		}
	}()
	for time.Now().Before(deadline) {
		_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
		if os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat))
	state := "unknown"
	if len(fields) > 2 {
		state = fields[2]
	}
	t.Fatalf("browser helper was not reaped; process state=%s", state)
}

func TestOpenBrowserReturnsLaunchFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fixture replaces Linux browser helper")
	}
	t.Setenv("PATH", t.TempDir())
	if err := OpenBrowser("http://127.0.0.1/"); err == nil {
		t.Fatal("expected missing browser helper error")
	}
}
