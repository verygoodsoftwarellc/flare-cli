package command

import (
	"bytes"
	"context"
	"errors"
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
		{name: "claude", args: []string{"mcp", "get", "flare"}},
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
	output := &bytes.Buffer{}
	var commands []recordedCommand
	installer := testMCPInstaller(output, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		if name == "codex" {
			return `{"transport":{"type":"stdio","command":"/usr/local/bin/flare-cli","args":["mcp"]}}`, nil
		}
		return "flare:\n  Scope: User config (available in all your projects)\n  Command: /usr/local/bin/flare-cli\n  Args: mcp", nil
	})

	if err := installer.install(context.Background(), []string{"codex", "claude"}, false, false); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("ran %d commands, want only two get commands: %#v", len(commands), commands)
	}
	if !strings.Contains(output.String(), "already configured") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMCPInstallerRequiresForceToReplaceConfiguration(t *testing.T) {
	installer := testMCPInstaller(&bytes.Buffer{}, func(_ context.Context, _ string, _ ...string) (string, error) {
		return `{"transport":{"type":"stdio","command":"/other/flare-cli","args":["mcp"]}}`, nil
	})
	err := installer.install(context.Background(), []string{"codex"}, false, false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
}

func TestMCPInstallerForceReplacesClaudeConfiguration(t *testing.T) {
	var commands []recordedCommand
	installer := testMCPInstaller(&bytes.Buffer{}, func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, recordedCommand{name: name, args: append([]string(nil), args...)})
		return "Command: /other/flare-cli\nArgs: mcp", nil
	})
	if err := installer.install(context.Background(), []string{"claude"}, true, false); err != nil {
		t.Fatal(err)
	}
	want := []recordedCommand{
		{name: "claude", args: []string{"mcp", "get", "flare"}},
		{name: "claude", args: []string{"mcp", "remove", "flare", "-s", "user"}},
		{name: "claude", args: []string{"mcp", "add", "-s", "user", "flare", "--", "/usr/local/bin/flare-cli", "mcp"}},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestMCPInstallerDryRunDoesNotExecuteCommands(t *testing.T) {
	output := &bytes.Buffer{}
	installer := testMCPInstaller(output, func(_ context.Context, _ string, _ ...string) (string, error) {
		t.Fatal("dry run executed a command")
		return "", nil
	})
	if err := installer.install(context.Background(), []string{"codex"}, false, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Would configure codex to run /usr/local/bin/flare-cli mcp") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMCPInstallerRejectsUnsupportedOrMissingClients(t *testing.T) {
	installer := testMCPInstaller(&bytes.Buffer{}, nil)
	if err := installer.install(context.Background(), []string{"cursor"}, false, false); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported client error = %v", err)
	}
	installer.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if err := installer.install(context.Background(), nil, false, false); err == nil || !strings.Contains(err.Error(), "no supported MCP clients") {
		t.Fatalf("missing clients error = %v", err)
	}
}

func TestMCPInstallerDoesNotTreatUnexpectedInspectionFailureAsMissing(t *testing.T) {
	installer := testMCPInstaller(&bytes.Buffer{}, func(_ context.Context, _ string, _ ...string) (string, error) {
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

func testMCPInstaller(output *bytes.Buffer, run func(context.Context, string, ...string) (string, error)) *mcpInstaller {
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
