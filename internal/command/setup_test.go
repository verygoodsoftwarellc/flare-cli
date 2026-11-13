package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

const testRemoteURL = "https://flare.example/mcp"

type fakeRemote struct {
	commands []recordedCommand
	attached []recordedCommand
	signedIn int
	verified []string
	codexGet string
	codexErr error
}

// newTestRemoteSetup isolates HOME and the Claude Code config, finds the
// claude and codex CLIs, and records everything it would run.
func newTestRemoteSetup(t *testing.T, output *bytes.Buffer, fake *fakeRemote) *remoteSetup {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(name, home)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if fake.codexGet == "" && fake.codexErr == nil {
		fake.codexGet, fake.codexErr = "Error: No MCP server named 'flare' found.", errors.New("exit status 1")
	}
	installer := &mcpInstaller{
		output: output,
		lookPath: func(name string) (string, error) {
			if name == "codex" || name == "claude" {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		run: func(_ context.Context, name string, args ...string) (string, error) {
			fake.commands = append(fake.commands, recordedCommand{name: name, args: append([]string(nil), args...)})
			if name == "codex" && args[1] == "get" {
				return fake.codexGet, fake.codexErr
			}
			return "", nil
		},
		runAttached: func(_ context.Context, name string, args ...string) error {
			fake.attached = append(fake.attached, recordedCommand{name: name, args: append([]string(nil), args...)})
			return nil
		},
	}
	return &remoteSetup{
		installer: installer,
		url:       testRemoteURL,
		signIn: func(context.Context) (string, error) {
			fake.signedIn++
			return "flare_pat_secret", nil
		},
		verify: func(_ context.Context, endpoint, token string) (int, error) {
			fake.verified = append(fake.verified, endpoint+" "+token)
			return 11, nil
		},
	}
}

func cursorConfigPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".cursor", "mcp.json")
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCursorServers(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(cursorConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	servers, _ := document["mcpServers"].(map[string]any)
	return document, servers
}

func TestRemoteSetupConfiguresEveryDetectedClientForOAuth(t *testing.T) {
	output := &bytes.Buffer{}
	fake := &fakeRemote{}
	setup := newTestRemoteSetup(t, output, fake)
	writeTestFile(t, cursorConfigPath(t), `{"mcpServers":{"other":{"url":"https://other.example/mcp?a=1&b=2"}},"theme":"dark"}`)
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDirectory, "Claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := setup.run(context.Background()); err != nil {
		t.Fatal(err)
	}

	wantCommands := []recordedCommand{
		{name: "claude", args: []string{"mcp", "add", "--transport", "http", "-s", "user", "flare", testRemoteURL}},
		{name: "codex", args: []string{"mcp", "get", "flare", "--json"}},
	}
	if !reflect.DeepEqual(fake.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", fake.commands, wantCommands)
	}
	wantAttached := []recordedCommand{{name: "codex", args: []string{"mcp", "add", "flare", "--url", testRemoteURL}}}
	if !reflect.DeepEqual(fake.attached, wantAttached) {
		t.Fatalf("attached = %#v, want %#v", fake.attached, wantAttached)
	}
	if fake.signedIn != 1 || !reflect.DeepEqual(fake.verified, []string{testRemoteURL + " flare_pat_secret"}) {
		t.Fatalf("signedIn = %d, verified = %v", fake.signedIn, fake.verified)
	}

	document, servers := readCursorServers(t)
	if document["theme"] != "dark" || servers["other"] == nil {
		t.Fatalf("Cursor's other settings were not kept: %#v", document)
	}
	if !reflect.DeepEqual(servers["flare"], map[string]any{"url": testRemoteURL}) {
		t.Fatalf("Cursor flare entry = %#v", servers["flare"])
	}
	data, _ := os.ReadFile(cursorConfigPath(t))
	if !strings.Contains(string(data), "a=1&b=2") {
		t.Fatalf("Cursor's other URL was escaped: %s", data)
	}

	got := output.String()
	for _, want := range []string{
		"Configured Flare for Claude Code",
		"Configured Flare for Codex",
		"Configured Flare for Cursor in ~/.cursor/mcp.json",
		"Checked " + testRemoteURL + ": 11 tools",
		"Claude Code: run /mcp",
		"Codex: run `codex mcp login flare`",
		"Cursor: open Cursor Settings",
		"Claude Desktop: in Settings → Connectors, choose Add custom connector and enter " + testRemoteURL,
		"Then ask:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "flare_pat_secret") {
		t.Fatalf("output shows the token:\n%s", got)
	}
}

func TestRemoteSetupLeavesMatchingConfigurationsAlone(t *testing.T) {
	output := &bytes.Buffer{}
	fake := &fakeRemote{codexGet: `{"enabled":true,"transport":{"type":"streamable_http","url":"` + testRemoteURL + `","bearer_token_env_var":null,"http_headers":null}}`}
	setup := newTestRemoteSetup(t, output, fake)
	writeTestFile(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json"), `{"mcpServers":{"flare":{"type":"http","url":"`+testRemoteURL+`"}}}`)
	cursor := `{"mcpServers":{"flare":{"url":"` + testRemoteURL + `"}}}`
	writeTestFile(t, cursorConfigPath(t), cursor)

	if err := setup.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.commands) != 1 || fake.commands[0].args[1] != "get" || len(fake.attached) != 0 {
		t.Fatalf("commands = %#v, attached = %#v; want only the Codex get", fake.commands, fake.attached)
	}
	if data, _ := os.ReadFile(cursorConfigPath(t)); string(data) != cursor {
		t.Fatalf("Cursor config was rewritten: %s", data)
	}
	if got := strings.Count(output.String(), "already configured"); got != 3 {
		t.Fatalf("output = %q", output.String())
	}
	if strings.Contains(output.String(), "Next:") || !strings.Contains(output.String(), "Ask: ") {
		t.Fatalf("already configured clients should need nothing more: %q", output.String())
	}
}

func TestRemoteSetupNeedsForceToReplaceAndKeepsGoing(t *testing.T) {
	output := &bytes.Buffer{}
	fake := &fakeRemote{}
	setup := newTestRemoteSetup(t, output, fake)
	claudeConfig := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json")
	writeTestFile(t, claudeConfig, `{"mcpServers":{"flare":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"]}}}`)
	localCursor := `{"mcpServers":{"flare":{"command":"/usr/local/bin/flare-cli","args":["mcp"]}}}`
	writeTestFile(t, cursorConfigPath(t), localCursor)

	err := setup.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Claude Code already has a different Flare MCP configuration; rerun with --force") ||
		!strings.Contains(err.Error(), "Cursor already has a different Flare MCP configuration") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.attached) != 1 || len(fake.verified) != 1 {
		t.Fatalf("Codex should still be configured and the server checked: attached = %#v, verified = %v", fake.attached, fake.verified)
	}
	if data, _ := os.ReadFile(cursorConfigPath(t)); string(data) != localCursor {
		t.Fatalf("Cursor config changed without --force: %s", data)
	}

	fake.commands, fake.attached = nil, nil
	setup.force = true
	if err := setup.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantCommands := []recordedCommand{
		{name: "claude", args: []string{"mcp", "remove", "flare", "-s", "user"}},
		{name: "claude", args: []string{"mcp", "add", "--transport", "http", "-s", "user", "flare", testRemoteURL}},
		{name: "codex", args: []string{"mcp", "get", "flare", "--json"}},
	}
	if !reflect.DeepEqual(fake.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", fake.commands, wantCommands)
	}
	if _, servers := readCursorServers(t); !reflect.DeepEqual(servers["flare"], map[string]any{"url": testRemoteURL}) {
		t.Fatalf("Cursor flare entry = %#v", servers["flare"])
	}
}

func TestRemoteSetupWithTokenWritesHeadersWithoutPrintingTheToken(t *testing.T) {
	output := &bytes.Buffer{}
	fake := &fakeRemote{}
	setup := newTestRemoteSetup(t, output, fake)
	setup.useToken = true
	setup.clients = []string{"claude", "codex", "cursor"}

	if err := setup.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantClaude := recordedCommand{name: "claude", args: []string{"mcp", "add", "--transport", "http", "-s", "user", "flare", testRemoteURL, "--header", "Authorization: Bearer flare_pat_secret"}}
	if !reflect.DeepEqual(fake.commands[0], wantClaude) {
		t.Fatalf("claude command = %#v", fake.commands[0])
	}
	wantCodex := recordedCommand{name: "codex", args: []string{"mcp", "add", "flare", "--url", testRemoteURL, "--bearer-token-env-var", "FLARE_TOKEN"}}
	if !reflect.DeepEqual(fake.attached, []recordedCommand{wantCodex}) {
		t.Fatalf("attached = %#v", fake.attached)
	}
	_, servers := readCursorServers(t)
	want := map[string]any{"url": testRemoteURL, "headers": map[string]any{"Authorization": "Bearer flare_pat_secret"}}
	if !reflect.DeepEqual(servers["flare"], want) {
		t.Fatalf("Cursor flare entry = %#v", servers["flare"])
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cursorConfigPath(t))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("Cursor config holding a token should be mode 0600: %v %v", info.Mode(), err)
		}
	}
	if strings.Contains(output.String(), "flare_pat_secret") {
		t.Fatalf("output shows the token:\n%s", output.String())
	}
	if strings.Contains(output.String(), "Claude Code:") || !strings.Contains(output.String(), "Codex: set FLARE_TOKEN") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRemoteSetupDryRunChangesNothing(t *testing.T) {
	output := &bytes.Buffer{}
	fake := &fakeRemote{}
	setup := newTestRemoteSetup(t, output, fake)
	setup.dryRun = true
	setup.useToken = true
	setup.clients = []string{"claude", "codex", "cursor", "claude-desktop"}

	if err := setup.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.signedIn != 0 || len(fake.verified) != 0 || len(fake.attached) != 0 {
		t.Fatalf("dry run signed in, verified or ran: %+v", fake)
	}
	if len(fake.commands) != 1 || fake.commands[0].args[1] != "get" {
		t.Fatalf("dry run should only inspect Codex, ran %#v", fake.commands)
	}
	if _, err := os.Stat(cursorConfigPath(t)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote Cursor config: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"Would run: claude mcp add --transport http -s user flare " + testRemoteURL + " --header 'Authorization: Bearer <your Flare token>'",
		"Would run: codex mcp add flare --url " + testRemoteURL + " --bearer-token-env-var FLARE_TOKEN",
		`Would set mcpServers.flare in ~/.cursor/mcp.json to {"url":"` + testRemoteURL + `","headers":{"Authorization":"Bearer <your Flare token>"}}`,
		"Would check " + testRemoteURL,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

func TestRemoteSetupRefusesUnreadableCursorConfig(t *testing.T) {
	output := &bytes.Buffer{}
	fake := &fakeRemote{}
	setup := newTestRemoteSetup(t, output, fake)
	setup.clients = []string{"cursor"}
	writeTestFile(t, cursorConfigPath(t), `{"mcpServers": {`)

	err := setup.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "~/.cursor/mcp.json") {
		t.Fatalf("error = %v", err)
	}
	if data, _ := os.ReadFile(cursorConfigPath(t)); string(data) != `{"mcpServers": {` {
		t.Fatalf("broken Cursor config was overwritten: %s", data)
	}
	if strings.Contains(output.String(), "Then ask") {
		t.Fatalf("nothing is configured, so there is nothing to ask yet: %q", output.String())
	}
}

func TestRemoteSetupClientSelection(t *testing.T) {
	setup := newTestRemoteSetup(t, &bytes.Buffer{}, &fakeRemote{})
	installer := setup.installer

	clients, err := installer.resolveClients(nil, remoteMCPClients, nil)
	if err != nil || !reflect.DeepEqual(clients, []string{"claude", "codex"}) {
		t.Fatalf("detected %v, %v; want claude and codex only", clients, err)
	}
	if clients, err := installer.resolveClients([]string{"Cursor", "cursor", "claude-desktop"}, remoteMCPClients, nil); err != nil || !reflect.DeepEqual(clients, []string{"cursor", "claude-desktop"}) {
		t.Fatalf("named clients = %v, %v", clients, err)
	}
	if _, err := installer.resolveClients([]string{"vscode"}, remoteMCPClients, nil); err == nil || !strings.Contains(err.Error(), "unsupported MCP client") {
		t.Fatalf("error = %v", err)
	}

	installer.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if _, err := installer.resolveClients([]string{"codex"}, remoteMCPClients, nil); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("error = %v", err)
	}
	if err := setup.run(context.Background()); err == nil || !strings.Contains(err.Error(), testRemoteURL) {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifyRemoteMCPSendsTheTokenAndListsTools(t *testing.T) {
	server := newMCPServer(api.New("http://127.0.0.1:1", "unused"))
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer flare_pat_good" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer remote.Close()

	count, err := verifyRemoteMCP(context.Background(), remote.URL, "flare_pat_good")
	if err != nil || count != 11 {
		t.Fatalf("count = %d, err = %v", count, err)
	}
	if _, err := verifyRemoteMCP(context.Background(), remote.URL, "flare_pat_bad"); err == nil {
		t.Fatal("expected a rejected token to fail")
	}
}

func TestMCPInstallTokenNeedsRemote(t *testing.T) {
	root := newRootCommand("flare-cli")
	root.SetArgs([]string{"mcp", "install", "--token", "--dry-run"})
	root.SetOut(&bytes.Buffer{})
	if err := root.ExecuteContext(context.Background()); err == nil || err.Error() != "--token works only with --remote" {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifyRemoteMCPDoesNotFollowRedirectsWithTheToken(t *testing.T) {
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		http.Error(writer, "nope", http.StatusNotFound)
	}))
	defer elsewhere.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, elsewhere.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer remote.Close()

	if _, err := verifyRemoteMCP(context.Background(), remote.URL, "flare_pat_good"); err == nil {
		t.Fatal("expected a redirect to fail the check")
	}
	if leaked.Load() {
		t.Fatal("the token followed the redirect to another host")
	}
}

func TestRemoteSetupMakesAMatchingCursorTokenConfigPrivate(t *testing.T) {
	setup := newTestRemoteSetup(t, &bytes.Buffer{}, &fakeRemote{})
	setup.useToken = true
	setup.clients = []string{"cursor"}
	cursor := `{"mcpServers":{"flare":{"url":"` + testRemoteURL + `","headers":{"Authorization":"Bearer flare_pat_secret"}}}}`
	writeTestFile(t, cursorConfigPath(t), cursor)
	if err := os.Chmod(cursorConfigPath(t), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := setup.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cursorConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(cursorConfigPath(t)); string(data) != cursor {
		t.Fatalf("Cursor config was rewritten: %s", data)
	}
}
