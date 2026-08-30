package config

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestDeleteTokenReportsKeyringFailureAndClearsFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalDelete := keyringDelete
	keyringDelete = func(_, _ string) error { return errors.New("locked") }
	t.Cleanup(func() { keyringDelete = originalDelete })

	settings := &Config{APIURL: "https://flare.test", Token: "flare_pat_fallback"}
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	err := settings.DeleteToken()
	if err == nil || !strings.Contains(err.Error(), "credential store") {
		t.Fatalf("expected credential-store error, got %v", err)
	}
	path, pathErr := Path()
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(data), "flare_pat_fallback") {
		t.Fatal("fallback token was not cleared")
	}
}

func TestDeleteTokenAllowsMissingKeyringEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalDelete := keyringDelete
	keyringDelete = func(_, _ string) error { return keyring.ErrNotFound }
	t.Cleanup(func() { keyringDelete = originalDelete })

	settings := &Config{APIURL: "https://flare.test"}
	if err := settings.DeleteToken(); err != nil {
		t.Fatal(err)
	}
}
