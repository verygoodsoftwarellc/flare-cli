package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordedCommand struct {
	name string
	args []string
}

func TestMCPInstallerConfiguresDetectedClients(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	output := &bytes.Buffer{}
	var commands []recordedCommand
	installer := &mcpInstaller{
		executable: "/usr/local/bin/flare-cli",
		output:     output,
		lookPath: func(name string) (string, error) {
			if name == "codex" || name == "claude" {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		run: func(_ context.Context, name string, args ...string) (string, error) {
			commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
			if len(args) >= 2 && args[1] == "get" {
				return "MCP server not found", errors.New("exit status 1")
			}
			return "", nil
		},
	}

	if err := installer.install(context.Background(), nil, false, false); err != nil {
		t.Fatal(err)
	}

	want := []recordedCommand{
		{name: "codex", args: []string{"mcp", "get", "flare", "--json"}},
		{name: "codex", args: []string{"mcp", "add", "flare", "--", "/usr/local/bin/flare-cli", "mcp"}},
		{name: "claude", args: []string{"mcp", "add", "-s", "user", "flare", "--", "/usr/local/bin/flare-cli", "mcp"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if got := output.String(); !strings.Contains(got, "Configured Flare for Codex") || !strings.Contains(got, "Configured Flare for Claude Code") {
		t.Fatalf("output = %q", got)
	}
}

func TestMCPInstallerLeavesMatchingConfigurationsAlone(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
	configuration := []byte(`{"mcpServers":{"flare":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"],"env":{}}}}`)
	if err := os.WriteFile(filepath.Join(directory, ".claude.json"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	var commands []recordedCommand
	installer := testMCPInstaller(t, output, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		if name == "codex" {
			return `{"enabled":true,"transport":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"]}}`, nil
		}
		return "", nil
	})

	if err := installer.install(context.Background(), []string{"codex", "claude"}, false, false); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].name != "codex" {
		t.Fatalf("commands = %#v, want only the Codex get command", commands)
	}
	if !strings.Contains(output.String(), "already configured") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMCPInstallerRequiresForceToReplaceConfiguration(t *testing.T) {
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, _ string, _ ...string) (string, error) {
		return `{"enabled":true,"transport":{"type":"stdio","command":"/other/flare-cli","args":["mcp"]}}`, nil
	})
	err := installer.install(context.Background(), []string{"codex"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
}

func TestMCPInstallerForceReplacesClaudeConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
	configuration := []byte(`{"mcpServers":{"flare":{"type":"stdio","command":"/other/flare-cli","args":["mcp"]}}}`)
	if err := os.WriteFile(filepath.Join(directory, ".claude.json"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	var commands []recordedCommand
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		return "Scope: User config (available in all your projects)\nCommand: /other/flare-cli\nArgs: mcp", nil
	})
	if err := installer.install(context.Background(), []string{"claude"}, true, false); err != nil {
		t.Fatal(err)
	}
	want := []recordedCommand{
		{name: "claude", args: []string{"mcp", "remove", "flare", "-s", "user"}},
		{name: "claude", args: []string{"mcp", "add", "-s", "user", "flare", "--", "/usr/local/bin/flare-cli", "mcp"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestMCPInstallerAddsUserConfigurationWhenOnlyProjectConfigurationExists(t *testing.T) {
	var commands []recordedCommand
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		return "", nil
	})
	if err := installer.install(context.Background(), []string{"claude"}, false, false); err != nil {
		t.Fatal(err)
	}
	want := []recordedCommand{
		{name: "claude", args: []string{"mcp", "add", "-s", "user", "flare", "--", "/usr/local/bin/flare-cli", "mcp"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestMCPInstallerEnablesMatchingDisabledCodexConfiguration(t *testing.T) {
	var commands []recordedCommand
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		if len(args) > 1 && args[1] == "get" {
			return `{"enabled":false,"transport":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"]}}`, nil
		}
		return "", nil
	})
	if err := installer.install(context.Background(), []string{"codex"}, false, false); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[1].args[1] != "add" {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestMCPInstallerTreatsEmbeddedEnvironmentAsDifferent(t *testing.T) {
	codex := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, _ string, _ ...string) (string, error) {
		return `{"enabled":true,"transport":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"],"env":{"FLARE_TOKEN":"flare_pat_old"}}}`, nil
	})
	if err := codex.install(context.Background(), []string{"codex"}, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Codex environment override error = %v", err)
	}

	directory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
	configuration := []byte(`{"mcpServers":{"flare":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"],"env":{"FLARE_TOKEN":"flare_pat_old"}}}}`)
	if err := os.WriteFile(filepath.Join(directory, ".claude.json"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	claude := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, _ string, _ ...string) (string, error) {
		return "", nil
	})
	if err := claude.install(context.Background(), []string{"claude"}, false, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Claude environment override error = %v", err)
	}
}

func TestMCPInstallerRunnerDoesNotMixSuccessfulStderrIntoStdout(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "mcp-runner-helper" {
		fmt.Fprint(os.Stdout, `{"enabled":true}`)
		fmt.Fprint(os.Stderr, "WARNING: harmless warning\n")
		os.Exit(0)
	}
	installer := newMCPInstaller(&bytes.Buffer{})
	output, err := installer.run(context.Background(), os.Args[0], "-test.run=TestMCPInstallerRunnerDoesNotMixSuccessfulStderrIntoStdout", "--", "mcp-runner-helper")
	if err != nil {
		t.Fatal(err)
	}
	if output != `{"enabled":true}` {
		t.Fatalf("output = %q", output)
	}
}

func TestMCPInstallerDryRunInspectsAndReportsMissingConfiguration(t *testing.T) {
	output := &bytes.Buffer{}
	var commands []recordedCommand
	installer := testMCPInstaller(t, output, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		return "MCP server not found", errors.New("exit status 1")
	})
	if err := installer.install(context.Background(), []string{"codex"}, false, true); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].args[1] != "get" {
		t.Fatalf("dry run commands = %#v", commands)
	}
	if !strings.Contains(output.String(), "Would configure Codex to run /usr/local/bin/flare-cli mcp") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMCPInstallerDryRunReportsNoOpAndForcedReplacement(t *testing.T) {
	output := &bytes.Buffer{}
	installer := testMCPInstaller(t, output, func(_ context.Context, _ string, _ ...string) (string, error) {
		return `{"enabled":true,"transport":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"]}}`, nil
	})
	if err := installer.install(context.Background(), []string{"codex"}, false, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "already configured") {
		t.Fatalf("output = %q", output.String())
	}

	output.Reset()
	installer.run = func(_ context.Context, _ string, _ ...string) (string, error) {
		return `{"enabled":true,"transport":{"type":"stdio","command":"/other/flare-cli","args":["mcp"]}}`, nil
	}
	if err := installer.install(context.Background(), []string{"codex"}, true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Would replace") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMCPInstallerRestoresClaudeConfigurationWhenReplacementFails(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
	path := filepath.Join(directory, ".claude.json")
	original := []byte("{\n  \"mcpServers\": {\"flare\": {\"command\": \"/old/flare\"}}\n}\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[1] {
		case "get":
			return "Scope: User config (available in all your projects)\nCommand: /old/flare\nArgs: mcp", nil
		case "remove":
			if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return "", nil
		case "add":
			return "add failed", errors.New("exit status 1")
		default:
			return "", errors.New("unexpected command")
		}
	})
	err := installer.install(context.Background(), []string{"claude"}, true, false)
	if err == nil || !strings.Contains(err.Error(), "add failed") {
		t.Fatalf("error = %v", err)
	}
	restored, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored configuration = %q, want %q", restored, original)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %v", info.Mode().Perm())
	}
}

func TestMCPInstallerRollbackPreservesSymlinkedClaudeConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", directory)
	target := filepath.Join(t.TempDir(), "claude.json")
	original := []byte(`{"mcpServers":{"flare":{"type":"stdio","command":"/old/flare","args":["mcp"]}}}`)
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, ".claude.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, _ string, args ...string) (string, error) {
		if args[1] == "add" {
			if err := os.WriteFile(target, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return "add failed", errors.New("exit status 1")
		}
		return "", nil
	})
	if err := installer.install(context.Background(), []string{"claude"}, true, false); err == nil {
		t.Fatal("expected replacement failure")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Claude config symlink was replaced: %v", err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("restored configuration = %q, want %q", restored, original)
	}
}

func TestMCPInstallerRejectsUnsupportedOrMissingClients(t *testing.T) {
	installer := testMCPInstaller(t, &bytes.Buffer{}, nil)
	if err := installer.install(context.Background(), []string{"cursor"}, false, false); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported client error = %v", err)
	}
	installer.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if err := installer.install(context.Background(), nil, false, false); err == nil || !strings.Contains(err.Error(), "no supported MCP clients") {
		t.Fatalf("missing clients error = %v", err)
	}
}

func TestMCPInstallerDoesNotTreatUnexpectedInspectionFailureAsMissing(t *testing.T) {
	installer := testMCPInstaller(t, &bytes.Buffer{}, func(_ context.Context, _ string, _ ...string) (string, error) {
		return "permission denied", errors.New("exit status 1")
	})
	err := installer.install(context.Background(), []string{"codex"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestMCPConfigPrintsResolvedAbsolutePath(t *testing.T) {
	directory := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+oldPath)
	executable := filepath.Join(directory, "flare-cli")
	if err := os.WriteFile(executable, []byte(""), 0o700); err != nil {
		t.Fatal(err)
	}
	oldArgs := os.Args
	os.Args = []string{"flare-cli"}
	t.Cleanup(func() { os.Args = oldArgs })
	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", output: output}
	if err := newMCPConfigCommand(options).Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, executable) || strings.Contains(got, "flare_pat_") {
		t.Fatalf("config = %q", got)
	}
}

func testMCPInstaller(t *testing.T, output *bytes.Buffer, run func(context.Context, string, ...string) (string, error)) *mcpInstaller {
	t.Helper()
	if _, set := os.LookupEnv("CLAUDE_CONFIG_DIR"); !set {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	}
	return &mcpInstaller{
		executable: "/usr/local/bin/flare-cli",
		output:     output,
		lookPath: func(name string) (string, error) {
			if name == "codex" || name == "claude" {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		run: run,
	}
}
