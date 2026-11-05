package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestDeleteTokenReportsKeyringFailureForKeyringToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalDelete := keyringDelete
	keyringDelete = func(_, _ string) error { return errors.New("locked") }
	t.Cleanup(func() { keyringDelete = originalDelete })

	settings := &Config{APIURL: "https://flare.test", TokenBackend: tokenBackendKeyring}
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}
	_, err := settings.DeleteToken()
	if err == nil || !strings.Contains(err.Error(), "credential store") {
		t.Fatalf("expected credential-store error, got %v", err)
	}
}

func TestDeleteTokenSucceedsForFileTokenWithoutCredentialStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalDelete := keyringDelete
	keyringDelete = func(_, _ string) error { return errors.New("no credential store") }
	t.Cleanup(func() { keyringDelete = originalDelete })

	settings := &Config{APIURL: "https://flare.test", Token: "flare_pat_file", TokenBackend: tokenBackendFile, TokenAPIURL: "https://flare.test"}
	skipped, err := settings.DeleteToken()
	if err != nil || !skipped {
		t.Fatalf("expected logout to succeed and flag the skipped store, got %v, %v", skipped, err)
	}
	if settings.Token != "" || settings.TokenBackend != "" {
		t.Fatalf("token was not cleared: %#v", settings)
	}
	if _, err := settings.DeleteToken(); err != nil {
		t.Fatalf("expected a repeat logout to succeed, got %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "flare_pat_file") {
		t.Fatal("file token was not cleared")
	}
}

func TestDeleteTokenAllowsMissingKeyringEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalDelete := keyringDelete
	keyringDelete = func(_, _ string) error { return keyring.ErrNotFound }
	t.Cleanup(func() { keyringDelete = originalDelete })

	settings := &Config{APIURL: "https://flare.test"}
	if _, err := settings.DeleteToken(); err != nil {
		t.Fatal(err)
	}
}

func TestTokenValueHonorsExplicitFileBackend(t *testing.T) {
	originalGet := keyringGet
	keyringGet = func(_, _ string) (string, error) { return "flare_pat_stale", nil }
	t.Cleanup(func() { keyringGet = originalGet })

	settings := &Config{
		APIURL:       "https://flare.test",
		Token:        "flare_pat_current",
		TokenBackend: tokenBackendFile,
		TokenAPIURL:  "https://flare.test",
	}
	token, err := settings.TokenValue()
	if err != nil {
		t.Fatal(err)
	}
	if token != "flare_pat_current" {
		t.Fatalf("expected file token, got %q", token)
	}
}

func TestStoreTokenRecordsFileBackendWhenKeyringFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalSet := keyringSet
	keyringSet = func(_, _, _ string) error { return errors.New("unavailable") }
	t.Cleanup(func() { keyringSet = originalSet })

	settings := &Config{APIURL: "https://flare.test"}
	fallback, err := settings.StoreToken("flare_pat_current")
	if err != nil {
		t.Fatal(err)
	}
	if !fallback || settings.TokenBackend != tokenBackendFile {
		t.Fatalf("expected explicit file fallback, got %#v", settings)
	}
}

func TestLoadMigratesLegacyFileTokenBeforeConsultingKeyring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("{\"api_url\":\"https://flare.test\",\"token\":\"flare_pat_current\"}\n")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	originalGet := keyringGet
	keyringGet = func(_, _ string) (string, error) { return "flare_pat_stale", nil }
	t.Cleanup(func() { keyringGet = originalGet })

	settings, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	token, err := settings.TokenValue()
	if err != nil {
		t.Fatal(err)
	}
	if token != "flare_pat_current" {
		t.Fatalf("expected migrated file token, got %q", token)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"token_backend": "file"`) ||
		!strings.Contains(string(data), `"token_api_url": "https://flare.test"`) {
		t.Fatalf("legacy credential ownership was not persisted: %s", data)
	}
}

func TestAPIURLOverrideDoesNotReuseFileCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings := &Config{
		APIURL:       "https://flare.am",
		Token:        "flare_pat_production",
		TokenBackend: tokenBackendFile,
		TokenAPIURL:  "https://flare.am",
	}
	if err := settings.Save(); err != nil {
		t.Fatal(err)
	}

	overridden, err := Load("https://other.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overridden.TokenValue(); err == nil || !strings.Contains(err.Error(), "no stored credentials") {
		t.Fatalf("expected host-specific credential error, got %v", err)
	}
}

func TestAPIURLRequiresHTTPSExceptForLoopback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, value := range []string{"https://flare.example", "http://localhost:3200", "http://127.0.0.1:3200", "http://[::1]:3200"} {
		if _, err := Load(value); err != nil {
			t.Errorf("expected %s to be accepted: %v", value, err)
		}
	}
	if _, err := Load("http://flare.example"); err == nil {
		t.Fatal("expected plaintext non-loopback API URL to be rejected")
	}
}

// A Secret Service unlock prompt nobody answers blocks the call forever, so
// every credential store call has to give up on its own.
func TestCredentialStoreCallsGiveUpWhenTheStoreHangs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stuck, exited := make(chan struct{}), make(chan struct{}, 3)
	hang := func() { <-stuck; exited <- struct{}{} }
	originalGet, originalSet, originalDelete, originalTimeout := keyringGet, keyringSet, keyringDelete, keyringTimeout
	keyringGet = func(_, _ string) (string, error) { hang(); return "flare_pat_late", nil }
	keyringSet = func(_, _, _ string) error { hang(); return nil }
	keyringDelete = func(_, _ string) error { hang(); return nil }
	keyringTimeout = 10 * time.Millisecond
	t.Cleanup(func() {
		// Let the abandoned calls finish before putting the real store back.
		close(stuck)
		for range 3 {
			<-exited
		}
		keyringGet, keyringSet, keyringDelete, keyringTimeout = originalGet, originalSet, originalDelete, originalTimeout
	})

	returns := func(name string, call func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { call(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s is still waiting on the credential store", name)
		}
	}

	settings := &Config{APIURL: "https://flare.test"}
	var token string
	var err error
	returns("TokenValue", func() { token, err = settings.TokenValue() })
	if token != "" || !errors.Is(err, errKeyringTimeout) || !strings.Contains(err.Error(), "unlocked") {
		t.Fatalf("expected a credential store timeout, got %q, %v", token, err)
	}

	var fallback bool
	returns("StoreToken", func() { fallback, err = settings.StoreToken("flare_pat_current") })
	if err != nil || !fallback || settings.TokenBackend != tokenBackendFile || settings.Token != "flare_pat_current" {
		t.Fatalf("expected the file fallback, got %v, %v, %#v", fallback, err, settings)
	}

	settings.TokenBackend = tokenBackendKeyring
	returns("DeleteToken", func() { _, err = settings.DeleteToken() })
	if !errors.Is(err, errKeyringTimeout) {
		t.Fatalf("expected a credential store timeout, got %v", err)
	}
}
