package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/config"
	"github.com/zalando/go-keyring"
)

func TestLoginDoesNotAcceptTokenInProcessArguments(t *testing.T) {
	root := newRootCommand("flare")
	authCommand, _, err := root.Find([]string{"auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	if authCommand.Flags().Lookup("token") != nil {
		t.Fatal("login still exposes a value-bearing --token flag")
	}
	if authCommand.Flags().Lookup("with-token") == nil {
		t.Fatal("login should accept tokens through stdin")
	}
}

func TestStrayArgumentsAreErrors(t *testing.T) {
	// If a case stops being rejected it runs for real, so nothing here may reach
	// the developer's stored credentials or a real server.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FLARE_API_URL", "http://127.0.0.1:1")
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	keyring.MockInit()
	run := func(args ...string) error {
		root := newRootCommand("flare-cli")
		root.SetOut(io.Discard)
		root.SetArgs(args)
		return root.Execute()
	}
	// Typos under a group suggest the closest command, as cobra does at the root.
	for _, test := range []struct {
		args       []string
		suggestion string
	}{
		{[]string{"auth", "statsu"}, "status"},
		{[]string{"auth", "lgout"}, "logout"},
		{[]string{"mcp", "instal"}, "install"},
		{[]string{"completion", "zhs"}, "zsh"},
		{[]string{"auth", "xyzzy"}, ""},
		{[]string{"auth", "status", "123"}, ""},
		{[]string{"mcp", "config", "789"}, ""},
		{[]string{"auth", "logout", "extra"}, ""},
	} {
		err := run(test.args...)
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%v: err = %v", test.args, err)
		} else if suggested := strings.Contains(err.Error(), "Did you mean this?\n\t"+test.suggestion+"\n"); suggested != (test.suggestion != "") {
			t.Errorf("%v: suggestion %q missing or unexpected in %q", test.args, test.suggestion, err)
		}
	}
	for _, args := range [][]string{{"auth"}, {"call", "--help"}, {"help", "tools"}, {"completion", "bash"}} {
		if err := run(args...); err != nil {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestAuthenticationErrorsUseInvokedExecutableName(t *testing.T) {
	root := newRootCommand("flare-cli")
	if root.Use != "flare-cli" {
		t.Fatalf("expected invoked executable in usage, got %q", root.Use)
	}

	options := &rootOptions{name: "flare-cli"}
	err := options.authenticationError(errors.New("not authenticated"))
	if got := err.Error(); got != "not authenticated; run `flare-cli auth login` or set FLARE_TOKEN" {
		t.Fatalf("unexpected authentication error %q", got)
	}
}

func TestExpiryNote(t *testing.T) {
	for expiresAt, want := range map[string]string{
		"2027-01-30T12:00:00Z": " (token expires 2027-01-30)",
		"2027-01-30T00:30:00Z": " (token expires 2027-01-29)",
		"":                     "",
		"soon":                 "",
	} {
		if got := expiryNote(expiresAt); got != want {
			t.Errorf("expiryNote(%q) = %q, want %q", expiresAt, got, want)
		}
	}
}

func TestVerifyAuthenticationChecksTokenWithServer(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		shouldFail bool
	}{
		{name: "valid token", status: http.StatusOK},
		{name: "valid token without org scope", status: http.StatusForbidden},
		{name: "invalid token", status: http.StatusUnauthorized, shouldFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/v1/organizations" || request.URL.Query().Get("limit") != "1" {
					t.Fatalf("unexpected status request %s", request.URL.String())
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer server.Close()

			err := verifyAuthentication(context.Background(), api.New(server.URL, "flare_pat_test"))
			if test.shouldFail && err == nil {
				t.Fatal("expected authentication check to fail")
			}
			if !test.shouldFail && err != nil {
				t.Fatalf("expected authentication check to pass, got %v", err)
			}
		})
	}
}

func TestUnauthorizedResponsesExplainHowToSignInAgain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"title":"Unauthorized","detail":"A valid personal access token is required."}`))
	}))
	defer server.Close()
	stored := &config.Config{APIURL: server.URL, Token: "flare_pat_expired", TokenBackend: "file", TokenAPIURL: server.URL}
	if err := stored.Save(); err != nil {
		t.Fatal(err)
	}

	const detail = "A valid personal access token is required. "
	const sameURL = " with the same --api-url"
	// A one-off --api-url is mentioned in words. FLARE_API_URL persists, so
	// the plain command already signs in to the same server.
	cases := []struct {
		flag, envToken bool
		want           string
	}{
		{flag: true, want: detail + "Run `flare-cli auth login`" + sameURL + " to sign in again."},
		{flag: true, envToken: true, want: detail + "FLARE_TOKEN was rejected; replace it, or unset it and run `flare-cli auth login`" + sameURL + "."},
		{want: detail + "Run `flare-cli auth login` to sign in again."},
		{envToken: true, want: detail + "FLARE_TOKEN was rejected; replace it, or unset it and run `flare-cli auth login`."},
	}
	for _, test := range cases {
		t.Setenv("FLARE_TOKEN", "")
		if test.envToken {
			t.Setenv("FLARE_TOKEN", "flare_pat_revoked")
		}
		t.Setenv("FLARE_API_URL", server.URL)
		commands := [][]string{{"tools"}, {"call", "list_organizations"}, {"auth", "status"}}
		if test.envToken {
			// Regression: ISSUE-001 — setup dropped the FLARE_TOKEN hint on a 401
			// Found by /qa on 2026-11-13
			// Report: .nunebot/qa-reports/qa-report-flare-cli-setup-2026-11-13.md
			// Without FLARE_TOKEN, setup signs in again instead of failing.
			commands = append(commands, []string{"setup", "--client", "cursor"})
		}
		for _, args := range commands {
			if test.flag {
				args = append(args, "--api-url", server.URL)
			}
			root := newRootCommand("flare-cli")
			root.SetArgs(args)
			if err := root.ExecuteContext(context.Background()); err == nil || err.Error() != test.want {
				t.Fatalf("%v (FLARE_TOKEN set: %v): expected %q, got %v", args, test.envToken, test.want, err)
			}
		}
	}
}

func TestLoginWithTokenChecksTheTokenBeforeSavingIt(t *testing.T) {
	tests := []struct {
		name   string
		status int
		saved  string
	}{
		{name: "accepted", status: http.StatusOK, saved: "flare_pat_new"},
		{name: "accepted without org scope", status: http.StatusForbidden, saved: "flare_pat_new"},
		{name: "rejected keeps the old token", status: http.StatusUnauthorized, saved: "flare_pat_old"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			keyring.MockInit()
			var sent string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				sent = request.Header.Get("Authorization")
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"detail":"A valid personal access token is required."}`))
			}))
			defer server.Close()
			settings, err := config.Load(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := settings.StoreToken("flare_pat_old"); err != nil {
				t.Fatal(err)
			}

			output := &bytes.Buffer{}
			options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}, input: strings.NewReader("flare_pat_new\n")}
			command := newAuthCommand(options)
			command.SetArgs([]string{"login", "--with-token"})
			err = command.Execute()

			if sent != "Bearer flare_pat_new" {
				t.Fatalf("checked %q, want the piped token", sent)
			}
			accepted := test.saved == "flare_pat_new"
			if accepted && err != nil {
				t.Fatal(err)
			}
			if !accepted && (err == nil || !strings.Contains(err.Error(), "token not saved: A valid personal access token is required.")) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(output.String(), "Authenticated") != accepted {
				t.Fatalf("output %q", output.String())
			}
			if saved, err := settings.TokenValue(); err != nil || saved != test.saved {
				t.Fatalf("saved %q (%v), want %q", saved, err, test.saved)
			}
		})
	}
}

func TestLoginAndLogoutWarnWhenFlareTokenStillWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_API_URL", server.URL)
	const warning = "FLARE_TOKEN is set and still takes priority"
	run := func(input string, args ...string) (string, string) {
		t.Helper()
		output, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		options := &rootOptions{name: "flare-cli", output: output, errors: stderr, input: strings.NewReader(input)}
		command := newAuthCommand(options)
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		return output.String(), stderr.String()
	}
	keyring.MockInit()

	for _, envToken := range []string{"", "flare_pat_env"} {
		t.Setenv("FLARE_TOKEN", envToken)
		for _, args := range [][]string{{"login", "--with-token"}, {"logout"}} {
			output, stderr := run("flare_pat_test", args...)
			if strings.Contains(stderr, warning) != (envToken != "") || strings.Contains(output, warning) {
				t.Fatalf("%v with FLARE_TOKEN=%q: stdout %q stderr %q", args, envToken, output, stderr)
			}
		}
	}
}
