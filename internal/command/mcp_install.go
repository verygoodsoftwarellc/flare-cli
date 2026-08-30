package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const mcpServerName = "flare"

type mcpInstaller struct {
	lookPath   func(string) (string, error)
	run        func(context.Context, string, ...string) (string, error)
	executable string
	output     io.Writer
}

type mcpClientConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type codexMCPConfig struct {
	Transport mcpClientConfig `json:"transport"`
}

func newMCPInstaller(output io.Writer) *mcpInstaller {
	return &mcpInstaller{
		lookPath: exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) (string, error) {
			command := exec.CommandContext(ctx, name, args...)
			result, err := command.CombinedOutput()
			return strings.TrimSpace(string(result)), err
		},
		output: output,
	}
}

func newMCPInstallCommand(options *rootOptions) *cobra.Command {
	var clients []string
	var dryRun bool
	var force bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Configure Flare in installed MCP clients",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !dryRun {
				if _, err := options.client(); err != nil {
					return err
				}
			}
			installer := newMCPInstaller(options.output)
			return installer.install(command.Context(), clients, force, dryRun)
		},
	}
	command.Flags().StringSliceVar(&clients, "client", nil, "MCP client to configure (codex or claude; repeatable)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "show changes without applying them")
	command.Flags().BoolVar(&force, "force", false, "replace an existing Flare MCP configuration")
	return command
}

func newMCPConfigCommand(options *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print a generic MCP configuration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			installer := newMCPInstaller(options.output)
			executable, err := installer.executablePath()
			if err != nil {
				return err
			}
			configuration := map[string]any{
				"mcpServers": map[string]mcpClientConfig{
					mcpServerName: {Command: executable, Args: []string{"mcp"}},
				},
			}
			encoder := json.NewEncoder(options.output)
			encoder.SetIndent("", "  ")
			return encoder.Encode(configuration)
		},
	}
}

func (installer *mcpInstaller) install(ctx context.Context, requested []string, force, dryRun bool) error {
	executable, err := installer.executablePath()
	if err != nil {
		return err
	}
	clients, err := installer.resolveClients(requested)
	if err != nil {
		return err
	}
	for _, client := range clients {
		if dryRun {
			fmt.Fprintf(installer.output, "Would configure %s to run %s mcp\n", client, executable)
			continue
		}
		switch client {
		case "codex":
			err = installer.installCodex(ctx, executable, force)
		case "claude":
			err = installer.installClaude(ctx, executable, force)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (installer *mcpInstaller) resolveClients(requested []string) ([]string, error) {
	if len(requested) == 0 {
		for _, name := range []string{"codex", "claude"} {
			if _, err := installer.lookPath(name); err == nil {
				requested = append(requested, name)
			}
		}
		if len(requested) == 0 {
			return nil, errors.New("no supported MCP clients found; use `flare-cli mcp config` for manual setup")
		}
	}
	seen := make(map[string]bool, len(requested))
	clients := make([]string, 0, len(requested))
	for _, name := range requested {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "codex" && name != "claude" {
			return nil, fmt.Errorf("unsupported MCP client %q; use codex or claude", name)
		}
		if seen[name] {
			continue
		}
		if _, err := installer.lookPath(name); err != nil {
			return nil, fmt.Errorf("%s is not installed or not on PATH", name)
		}
		seen[name] = true
		clients = append(clients, name)
	}
	return clients, nil
}

func (installer *mcpInstaller) installCodex(ctx context.Context, executable string, force bool) error {
	output, err := installer.run(ctx, "codex", "mcp", "get", mcpServerName, "--json")
	if err == nil {
		var current codexMCPConfig
		if json.Unmarshal([]byte(output), &current) == nil && sameMCPConfig(current.Transport, executable) {
			fmt.Fprintln(installer.output, "Codex is already configured for Flare")
			return nil
		}
		if !force {
			return errors.New("Codex already has a different Flare MCP configuration; rerun with --force to replace it")
		}
	} else if !mcpConfigMissing(output) {
		return fmt.Errorf("inspect Codex MCP configuration: %s", commandFailure(output, err))
	}
	output, err = installer.run(ctx, "codex", "mcp", "add", mcpServerName, "--", executable, "mcp")
	if err != nil {
		return fmt.Errorf("configure Codex: %s", commandFailure(output, err))
	}
	fmt.Fprintln(installer.output, "Configured Flare for Codex")
	return nil
}

func (installer *mcpInstaller) installClaude(ctx context.Context, executable string, force bool) error {
	output, err := installer.run(ctx, "claude", "mcp", "get", mcpServerName)
	if err == nil {
		if sameClaudeConfig(output, executable) {
			fmt.Fprintln(installer.output, "Claude Code is already configured for Flare")
			return nil
		}
		if !force {
			return errors.New("Claude Code already has a different Flare MCP configuration; rerun with --force to replace it")
		}
		removeOutput, removeErr := installer.run(ctx, "claude", "mcp", "remove", mcpServerName, "-s", "user")
		if removeErr != nil {
			return fmt.Errorf("remove existing Claude Code MCP configuration: %s", commandFailure(removeOutput, removeErr))
		}
	} else if !mcpConfigMissing(output) {
		return fmt.Errorf("inspect Claude Code MCP configuration: %s", commandFailure(output, err))
	}
	output, err = installer.run(ctx, "claude", "mcp", "add", "-s", "user", mcpServerName, "--", executable, "mcp")
	if err != nil {
		return fmt.Errorf("configure Claude Code: %s", commandFailure(output, err))
	}
	fmt.Fprintln(installer.output, "Configured Flare for Claude Code")
	return nil
}

func (installer *mcpInstaller) executablePath() (string, error) {
	if installer.executable != "" {
		return filepath.Abs(installer.executable)
	}
	if path, err := installer.lookPath(os.Args[0]); err == nil {
		return filepath.Abs(path)
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find flare-cli executable: %w", err)
	}
	return filepath.Abs(path)
}

func sameMCPConfig(config mcpClientConfig, executable string) bool {
	return config.Command == executable && len(config.Args) == 1 && config.Args[0] == "mcp"
}

func sameClaudeConfig(output, executable string) bool {
	var command, args string
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "command":
			command = strings.TrimSpace(value)
		case "args":
			args = strings.TrimSpace(value)
		}
	}
	return command == executable && args == "mcp"
}

func mcpConfigMissing(output string) bool {
	value := strings.ToLower(output)
	return strings.Contains(value, "not found") || strings.Contains(value, "no mcp server")
}

func commandFailure(output string, err error) string {
	if output != "" {
		return output
	}
	return err.Error()
}
