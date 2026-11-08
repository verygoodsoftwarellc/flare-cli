package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveKeepsConfigReadableDuringReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows fallback retains existing in-place writes")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	settings := &Config{APIURL: defaultAPIURL, TokenBackend: tokenBackendFile, Token: strings.Repeat("fixture", 4096), TokenAPIURL: defaultAPIURL}
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 200; i++ {
			if err := settings.Save(); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	invalidLength := -1
	var readError error
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if readError != nil {
				t.Fatal(readError)
			}
			if invalidLength >= 0 {
				t.Fatalf("concurrent reader saw incomplete config (%d bytes)", invalidLength)
			}
			return
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				readError = err
			} else if !json.Valid(data) {
				invalidLength = len(data)
			}
		}
	}
}

func TestSavePreservesConfigSymlinks(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "dangling"}[exists], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("APPDATA", t.TempDir())
			path, err := Path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "stored.json")
			if exists {
				if err := os.WriteFile(target, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlink unavailable:%v", err)
			}
			if err := (&Config{APIURL: defaultAPIURL}).Save(); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("config link was replaced")
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			var saved Config
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.APIURL != defaultAPIURL {
				t.Fatal("target was not updated")
			}
			info, err = os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatalf("target mode=%o", info.Mode().Perm())
			}
		})
	}
}

func TestSaveKeepsWritableFileInReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory modes")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings := &Config{APIURL: defaultAPIURL}
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(path)
	if err := os.Chmod(directory, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(directory, 0700)
	if probe, err := os.CreateTemp(directory, "permission-probe-"); err == nil {
		probe.Close()
		os.Remove(probe.Name())
		t.Skip("privileged process bypasses directory mode")
	}
	settings.APIURL = "http://127.0.0.1:4201"
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.APIURL != settings.APIURL {
		t.Fatal("config was not saved")
	}
}

func TestSaveStillRejectsReadOnlyConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings := &Config{APIURL: defaultAPIURL}
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if file, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		file.Close()
		t.Skip("privileged process bypasses file mode")
	}
	settings.APIURL = "http://127.0.0.1:4201"
	if err := settings.Save(); err == nil {
		t.Fatal("read-only config was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.APIURL != defaultAPIURL {
		t.Fatal("old config was changed")
	}
}
