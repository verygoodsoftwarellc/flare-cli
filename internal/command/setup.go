package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/version"
)

// remoteMCPClients are the clients `setup` and `mcp install --remote` can
// point at Flare's hosted MCP server, in the order they're configured.
var remoteMCPClients = []string{"claude", "codex", "cursor", "claude-desktop"}

var mcpClientLabels = map[string]string{
	"claude":         "Claude Code",
	"codex":          "Codex",
	"cursor":         "Cursor",
	"claude-desktop": "Claude Desktop",
}

// tokenPlaceholder stands in for the token in dry runs, which never read it.
const tokenPlaceholder = "<your Flare token>"

const firstPrompt = "Using Flare, what's slowest in production over the last 24 hours?"

// remoteSetup signs in, points each client at the hosted MCP server, and
// checks that the server answers. Clients sign in to the server themselves
// with OAuth, unless useToken writes the CLI's personal access token into
// their configuration instead.
type remoteSetup struct {
	installer *mcpInstaller
	url       string
	clients   []string
	useToken  bool
	force     bool
	dryRun    bool
	signIn    func(context.Context) (string, error)
	verify    func(ctx context.Context, endpoint, token string) (int, error)
}

// remoteInstall is what each client's installer needs to know.
type remoteInstall struct {
	url    string
	token  string
	force  bool
	dryRun bool
}

func newSetupCommand(options *rootOptions) *cobra.Command {
	var flags remoteFlags
	command := &cobra.Command{
		Use:   "setup",
		Short: "Sign in and connect your AI clients to Flare",
		Long: "Sign in to Flare, then point Claude Code, Codex, Cursor and Claude Desktop (whichever are installed) at Flare's MCP server.\n\n" +
			"Each client signs in to Flare itself the first time it connects. With --token, the CLI's personal access token is written into the client's configuration instead, for clients that can't sign in on their own.",
		RunE: func(command *cobra.Command, _ []string) error {
			return options.runRemoteSetup(command.Context(), flags)
		},
	}
	flags.register(command, "MCP client to configure (claude, codex, cursor or claude-desktop; repeatable; default: every one installed)")
	return command
}

type remoteFlags struct {
	clients  []string
	dryRun   bool
	force    bool
	useToken bool
}

func (flags *remoteFlags) register(command *cobra.Command, clientUsage string) {
	command.Flags().StringSliceVar(&flags.clients, "client", nil, clientUsage)
	command.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show changes without applying them")
	command.Flags().BoolVar(&flags.force, "force", false, "replace an existing Flare MCP configuration")
	command.Flags().BoolVar(&flags.useToken, "token", false, "write your personal access token into the configuration instead of having the client sign in")
}

func (options *rootOptions) runRemoteSetup(ctx context.Context, flags remoteFlags) error {
	settings, err := options.config()
	if err != nil {
		return err
	}
	setup := &remoteSetup{
		installer: newMCPInstaller(options.output),
		url:       strings.TrimRight(settings.APIURL, "/") + "/mcp",
		clients:   flags.clients,
		useToken:  flags.useToken,
		force:     flags.force,
		dryRun:    flags.dryRun,
		signIn:    options.ensureSignedIn,
		verify:    verifyRemoteMCP,
	}
	return setup.run(ctx)
}

func (setup *remoteSetup) run(ctx context.Context) error {
	output := setup.installer.output
	clients, err := setup.installer.resolveRemoteClients(setup.clients, setup.url)
	if err != nil {
		return err
	}

	token := tokenPlaceholder
	if setup.dryRun {
		fmt.Fprintln(output, "Would sign in to Flare if needed")
	} else if token, err = setup.signIn(ctx); err != nil {
		return err
	}
	remote := remoteInstall{url: setup.url, force: setup.force, dryRun: setup.dryRun}
	if setup.useToken {
		remote.token = token
	}

	var failures []error
	var next []string
	ready := 0
	for _, client := range clients {
		hint, err := setup.installer.installRemote(ctx, client, remote)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		ready++
		if hint != "" {
			next = append(next, mcpClientLabels[client]+": "+hint)
		}
	}

	if setup.dryRun {
		fmt.Fprintf(output, "Would check %s with initialize and tools/list\n", setup.url)
	} else if count, err := setup.verify(ctx, setup.url, token); err != nil {
		failures = append(failures, fmt.Errorf("check %s: %w", setup.url, err))
	} else {
		fmt.Fprintf(output, "Checked %s: %d tools\n", setup.url, count)
	}

	if ready > 0 {
		fmt.Fprintln(output)
		if len(next) > 0 {
			fmt.Fprintln(output, "Next:")
			for _, line := range next {
				fmt.Fprintln(output, "  "+line)
			}
			fmt.Fprintln(output)
		}
		lead := "Ask"
		if len(next) > 0 {
			lead = "Then ask"
		}
		fmt.Fprintf(output, "%s: %q\n", lead, firstPrompt)
	}
	return errors.Join(failures...)
}

// resolveRemoteClients returns the requested clients, or every one found on
// this machine. Claude Code and Codex are configured through their own CLIs,
// so those must be installed even when asked for by name.
func (installer *mcpInstaller) resolveRemoteClients(requested []string, url string) ([]string, error) {
	if len(requested) == 0 {
		for _, name := range remoteMCPClients {
			if installer.detect(name) {
				requested = append(requested, name)
			}
		}
		if len(requested) == 0 {
			return nil, fmt.Errorf("no supported MCP clients found; add %s to your MCP client as a remote (streamable HTTP) server", url)
		}
	}
	clients := make([]string, 0, len(requested))
	for _, name := range requested {
		name = strings.ToLower(strings.TrimSpace(name))
		if !slices.Contains(remoteMCPClients, name) {
			return nil, fmt.Errorf("unsupported MCP client %q; use %s", name, strings.Join(remoteMCPClients, ", "))
		}
		if slices.Contains(clients, name) {
			continue
		}
		if name == "claude" || name == "codex" {
			if _, err := installer.lookPath(name); err != nil {
				return nil, fmt.Errorf("%s is not installed or not on PATH", name)
			}
		}
		clients = append(clients, name)
	}
	return clients, nil
}

func (installer *mcpInstaller) detect(client string) bool {
	switch client {
	case "claude", "codex":
		_, err := installer.lookPath(client)
		return err == nil
	case "cursor":
		if _, err := installer.lookPath("cursor"); err == nil {
			return true
		}
		home, err := os.UserHomeDir()
		return err == nil && directoryExists(filepath.Join(home, ".cursor"))
	case "claude-desktop":
		directory, err := os.UserConfigDir()
		return err == nil && directoryExists(filepath.Join(directory, "Claude"))
	}
	return false
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// installRemote configures one client and returns what the user still has to
// do in it, if anything.
func (installer *mcpInstaller) installRemote(ctx context.Context, client string, remote remoteInstall) (string, error) {
	switch client {
	case "claude":
		return installer.installClaudeRemote(ctx, remote)
	case "codex":
		return installer.installCodexRemote(ctx, remote)
	case "cursor":
		return installer.installCursorRemote(remote)
	case "claude-desktop":
		hint := "in Settings → Connectors, choose Add custom connector and enter " + remote.url
		if remote.token != "" {
			hint += " (connectors sign in to Flare themselves; --token doesn't apply)"
		}
		return hint, nil
	}
	return "", fmt.Errorf("unsupported MCP client %q", client)
}

func (remote remoteInstall) headers() map[string]string {
	if remote.token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + remote.token}
}

func (installer *mcpInstaller) installClaudeRemote(ctx context.Context, remote remoteInstall) (string, error) {
	current, found, backup, err := readClaudeUserConfig()
	if err != nil {
		return "", fmt.Errorf("inspect Claude Code MCP configuration: %w", err)
	}
	args := []string{"mcp", "add", "--transport", "http", "-s", "user", mcpServerName, remote.url}
	if remote.token != "" {
		args = append(args, "--header", "Authorization: Bearer "+remote.token)
	}
	if found {
		if current.Type == "http" && current.URL == remote.url && reflect.DeepEqual(nonEmpty(current.Headers), remote.headers()) {
			fmt.Fprintln(installer.output, "Claude Code is already configured for Flare")
			return "", nil
		}
		if !remote.force {
			return "", errors.New("Claude Code already has a different Flare MCP configuration; rerun with --force to replace it")
		}
		if remote.dryRun {
			fmt.Fprintf(installer.output, "Would replace Claude Code's Flare configuration: claude %s\n", hideToken(shellJoin(args), remote.token))
			return claudeSignInHint(remote), nil
		}
		removeOutput, removeErr := installer.run(ctx, "claude", "mcp", "remove", mcpServerName, "-s", "user")
		if removeErr != nil {
			return "", fmt.Errorf("remove existing Claude Code MCP configuration: %s", commandFailure(removeOutput, removeErr))
		}
	} else if remote.dryRun {
		fmt.Fprintf(installer.output, "Would run: claude %s\n", hideToken(shellJoin(args), remote.token))
		return claudeSignInHint(remote), nil
	}
	output, err := installer.run(ctx, "claude", args...)
	if err != nil {
		failure := hideToken(commandFailure(output, err), remote.token)
		if backup != nil {
			if restoreErr := backup.restore(); restoreErr != nil {
				return "", fmt.Errorf("configure Claude Code: %s; restoring previous configuration also failed: %v", failure, restoreErr)
			}
		}
		return "", fmt.Errorf("configure Claude Code: %s", failure)
	}
	fmt.Fprintln(installer.output, "Configured Flare for Claude Code")
	return claudeSignInHint(remote), nil
}

func claudeSignInHint(remote remoteInstall) string {
	if remote.token != "" {
		return ""
	}
	return "run /mcp, choose flare, and sign in"
}

// installCodexRemote runs `codex mcp add` attached to the terminal, because
// Codex may start its OAuth sign-in right away and print a URL to open.
//
// Codex can't store a literal header from its CLI, so with --token it reads
// the token from FLARE_TOKEN, the variable flare-cli itself uses.
func (installer *mcpInstaller) installCodexRemote(ctx context.Context, remote remoteInstall) (string, error) {
	args := []string{"mcp", "add", mcpServerName, "--url", remote.url}
	var bearerVariable *string
	hint := "run `codex mcp login flare` if it hasn't asked you to sign in"
	if remote.token != "" {
		variable := "FLARE_TOKEN"
		bearerVariable = &variable
		args = append(args, "--bearer-token-env-var", variable)
		hint = "set FLARE_TOKEN to your token wherever Codex runs; Codex reads it from there"
	}
	output, err := installer.run(ctx, "codex", "mcp", "get", mcpServerName, "--json")
	if err == nil {
		var current codexMCPConfig
		if unmarshalErr := json.Unmarshal([]byte(output), &current); unmarshalErr != nil {
			return "", fmt.Errorf("inspect Codex MCP configuration: decode response: %w", unmarshalErr)
		}
		transport := current.Transport
		matching := transport.Type == "streamable_http" && transport.URL == remote.url &&
			reflect.DeepEqual(transport.BearerTokenEnvVar, bearerVariable) &&
			len(transport.HTTPHeaders) == 0 && len(transport.EnvHTTPHeaders) == 0
		if matching && current.Enabled {
			fmt.Fprintln(installer.output, "Codex is already configured for Flare")
			return "", nil
		}
		if !matching && !remote.force {
			return "", errors.New("Codex already has a different Flare MCP configuration; rerun with --force to replace it")
		}
		if remote.dryRun {
			fmt.Fprintf(installer.output, "Would replace Codex's Flare configuration: codex %s\n", strings.Join(args, " "))
			return hint, nil
		}
	} else if !mcpConfigMissing(output) {
		return "", fmt.Errorf("inspect Codex MCP configuration: %s", commandFailure(output, err))
	} else if remote.dryRun {
		fmt.Fprintf(installer.output, "Would run: codex %s\n", strings.Join(args, " "))
		return hint, nil
	}
	if err := installer.runAttached(ctx, "codex", args...); err != nil {
		return "", fmt.Errorf("configure Codex: %w", err)
	}
	fmt.Fprintln(installer.output, "Configured Flare for Codex")
	return hint, nil
}

type cursorMCPEntry struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// installCursorRemote sets mcpServers.flare in ~/.cursor/mcp.json, keeping
// every other key and server in the file.
func (installer *mcpInstaller) installCursorRemote(remote remoteInstall) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find Cursor configuration: %w", err)
	}
	path := filepath.Join(home, ".cursor", "mcp.json")
	shown := filepath.Join("~", ".cursor", "mcp.json")
	// Write through a symlinked file (dotfiles) rather than replacing the link.
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}

	document := map[string]json.RawMessage{}
	servers := map[string]json.RawMessage{}
	mode := os.FileMode(0o644)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &document); err != nil {
			return "", fmt.Errorf("read %s: %w", shown, err)
		}
		if raw, ok := document["mcpServers"]; ok {
			if err := json.Unmarshal(raw, &servers); err != nil {
				return "", fmt.Errorf("read mcpServers in %s: %w", shown, err)
			}
		}
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read %s: %w", shown, err)
	}

	entry, err := marshalJSON(cursorMCPEntry{URL: remote.url, Headers: remote.headers()}, "")
	if err != nil {
		return "", err
	}
	hint := "open Cursor Settings → MCP and sign in to flare"
	if remote.token != "" {
		hint = ""
	}
	if current, ok := servers[mcpServerName]; ok {
		if sameJSON(current, entry) {
			fmt.Fprintln(installer.output, "Cursor is already configured for Flare")
			return "", nil
		}
		if !remote.force {
			return "", fmt.Errorf("Cursor already has a different Flare MCP configuration in %s; rerun with --force to replace it", shown)
		}
	}
	if remote.dryRun {
		fmt.Fprintf(installer.output, "Would set mcpServers.flare in %s to %s\n", shown, hideToken(strings.TrimSpace(string(entry)), remote.token))
		return hint, nil
	}

	servers[mcpServerName] = entry
	if document["mcpServers"], err = marshalJSON(servers, ""); err != nil {
		return "", err
	}
	updated, err := marshalJSON(document, "  ")
	if err != nil {
		return "", err
	}
	if remote.token != "" {
		mode = 0o600
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("write %s: %w", shown, err)
	}
	if err := writeFileAtomic(path, updated, mode); err != nil {
		return "", fmt.Errorf("write %s: %w", shown, err)
	}
	fmt.Fprintf(installer.output, "Configured Flare for Cursor in %s\n", shown)
	return hint, nil
}

// marshalJSON leaves <, > and & as they are, so rewriting a client's file
// doesn't turn other entries' URLs into \u0026 escapes. It ends with a newline.
func marshalJSON(value any, indent string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", indent)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func nonEmpty(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return values
}

// shellJoin joins arguments for display, quoting any with spaces.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		if strings.ContainsAny(arg, " \t") {
			arg = "'" + arg + "'"
		}
		quoted[index] = arg
	}
	return strings.Join(quoted, " ")
}

// hideToken keeps the token out of anything printed.
func hideToken(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, tokenPlaceholder)
}

// verifyRemoteMCP connects to the hosted server the way a client does, with
// initialize and tools/list, and returns how many tools it offers.
func verifyRemoteMCP(ctx context.Context, endpoint, token string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "flare-cli", Version: version.Current()}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return 0, err
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return 0, err
	}
	return len(tools.Tools), nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	request.Header.Set("User-Agent", "flare-cli/"+version.Current())
	return transport.base.RoundTrip(request)
}

// ensureSignedIn returns a working token, signing in through the browser when
// there is none or the stored one has expired.
func (options *rootOptions) ensureSignedIn(ctx context.Context) (string, error) {
	settings, err := options.config()
	if err != nil {
		return "", err
	}
	if token, tokenErr := settings.TokenValue(); tokenErr == nil {
		err := verifyAuthentication(ctx, options.newClient(settings.APIURL, token))
		if err == nil {
			fmt.Fprintf(options.output, "Signed in to %s\n", settings.APIURL)
			return token, nil
		}
		var apiError *api.APIError
		if !errors.As(err, &apiError) || apiError.Status != http.StatusUnauthorized || os.Getenv("FLARE_TOKEN") != "" {
			return "", err
		}
		fmt.Fprintln(options.errors, "Your Flare sign-in has expired; signing in again.")
	}
	token, expires, err := options.browserLogin(ctx, settings.APIURL)
	if err != nil {
		return "", err
	}
	if err := options.storeToken(settings, token); err != nil {
		return "", err
	}
	fmt.Fprintf(options.output, "Signed in to %s%s\n", settings.APIURL, expires)
	return token, nil
}
