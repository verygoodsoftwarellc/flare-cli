package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

type codexMCPTransport struct {
	mcpClientConfig
	Type    string            `json:"type"`
	Env     map[string]string `json:"env"`
	EnvVars []string          `json:"env_vars"`
	Cwd     *string           `json:"cwd"`
}

type codexMCPConfig struct {
	Enabled   bool              `json:"enabled"`
	Transport codexMCPTransport `json:"transport"`
}

type claudeMCPEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type claudeUserConfig struct {
	MCPServers map[string]claudeMCPEntry `json:"mcpServers"`
}

type fileBackup struct {
	path string
	data []byte
	mode os.FileMode
}

func newMCPInstaller(output io.Writer) *mcpInstaller {
	return &mcpInstaller{
		lookPath: exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) (string, error) {
			command := exec.CommandContext(ctx, name, args...)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			result, err := command.Output()
			if err != nil && stderr.Len() > 0 {
				return strings.TrimSpace(string(result) + "\n" + stderr.String()), err
			}
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
		switch client {
		case "codex":
			err = installer.installCodex(ctx, executable, force, dryRun)
		case "claude":
			err = installer.installClaude(ctx, executable, force, dryRun)
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

func (installer *mcpInstaller) installCodex(ctx context.Context, executable string, force, dryRun bool) error {
	output, err := installer.run(ctx, "codex", "mcp", "get", mcpServerName, "--json")
	if err == nil {
		var current codexMCPConfig
		if unmarshalErr := json.Unmarshal([]byte(output), &current); unmarshalErr != nil {
			return fmt.Errorf("inspect Codex MCP configuration: decode response: %w", unmarshalErr)
		}
		matching := sameCodexConfig(current, executable)
		if matching && current.Enabled {
			fmt.Fprintln(installer.output, "Codex is already configured for Flare")
			return nil
		}
		if !matching && !force {
			return errors.New("Codex already has a different Flare MCP configuration; rerun with --force to replace it")
		}
		if dryRun {
			if matching {
				fmt.Fprintln(installer.output, "Would enable Flare for Codex")
			} else {
				fmt.Fprintf(installer.output, "Would replace Codex Flare configuration with %s mcp\n", executable)
			}
			return nil
		}
	} else if !mcpConfigMissing(output) {
		return fmt.Errorf("inspect Codex MCP configuration: %s", commandFailure(output, err))
	} else if dryRun {
		fmt.Fprintf(installer.output, "Would configure Codex to run %s mcp\n", executable)
		return nil
	}
	output, err = installer.run(ctx, "codex", "mcp", "add", mcpServerName, "--", executable, "mcp")
	if err != nil {
		return fmt.Errorf("configure Codex: %s", commandFailure(output, err))
	}
	fmt.Fprintln(installer.output, "Configured Flare for Codex")
	return nil
}

func (installer *mcpInstaller) installClaude(ctx context.Context, executable string, force, dryRun bool) error {
	current, found, backup, err := readClaudeUserConfig()
	if err != nil {
		return fmt.Errorf("inspect Claude Code MCP configuration: %w", err)
	}
	if found {
		if sameClaudeConfig(current, executable) {
			fmt.Fprintln(installer.output, "Claude Code is already configured for Flare")
			return nil
		}
		if !force {
			return errors.New("Claude Code already has a different Flare MCP configuration; rerun with --force to replace it")
		}
		if dryRun {
			fmt.Fprintf(installer.output, "Would replace Claude Code Flare configuration with %s mcp\n", executable)
			return nil
		}
		removeOutput, removeErr := installer.run(ctx, "claude", "mcp", "remove", mcpServerName, "-s", "user")
		if removeErr != nil {
			return fmt.Errorf("remove existing Claude Code MCP configuration: %s", commandFailure(removeOutput, removeErr))
		}
	} else if dryRun {
		fmt.Fprintf(installer.output, "Would configure Claude Code to run %s mcp\n", executable)
		return nil
	}
	output, err := installer.run(ctx, "claude", "mcp", "add", "-s", "user", mcpServerName, "--", executable, "mcp")
	if err != nil {
		if backup != nil {
			if restoreErr := backup.restore(); restoreErr != nil {
				return fmt.Errorf("configure Claude Code: %s; restoring previous configuration also failed: %v", commandFailure(output, err), restoreErr)
			}
		}
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

func sameCodexConfig(config codexMCPConfig, executable string) bool {
	return config.Transport.Type == "stdio" &&
		sameMCPConfig(config.Transport.mcpClientConfig, executable) &&
		len(config.Transport.Env) == 0 && len(config.Transport.EnvVars) == 0 && config.Transport.Cwd == nil
}

func sameClaudeConfig(config claudeMCPEntry, executable string) bool {
	return config.Type == "stdio" &&
		sameMCPConfig(mcpClientConfig{Command: config.Command, Args: config.Args}, executable) &&
		len(config.Env) == 0
}

func readClaudeUserConfig() (claudeMCPEntry, bool, *fileBackup, error) {
	directory := os.Getenv("CLAUDE_CONFIG_DIR")
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return claudeMCPEntry{}, false, nil, err
		}
		directory = home
	}
	path := filepath.Join(directory, ".claude.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return claudeMCPEntry{}, false, nil, nil
	}
	if err != nil {
		return claudeMCPEntry{}, false, nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return claudeMCPEntry{}, false, nil, err
	}
	backupPath := path
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		backupPath = resolved
	}
	var config claudeUserConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return claudeMCPEntry{}, false, nil, err
	}
	entry, found := config.MCPServers[mcpServerName]
	return entry, found, &fileBackup{path: backupPath, data: data, mode: info.Mode()}, nil
}

func (backup *fileBackup) restore() error {
	directory := filepath.Dir(backup.path)
	temporary, err := os.CreateTemp(directory, ".flare-claude-restore-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(backup.mode.Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(backup.data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, backup.path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	return os.WriteFile(backup.path, backup.data, backup.mode.Perm())
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
