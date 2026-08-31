# Flare CLI

Read application performance data from [Flare](https://flare.am).

## Install

```sh
go install github.com/verygoodsoftwarellc/flare-cli@latest
```

This installs `flare-cli` into `$(go env GOPATH)/bin`. Make sure that directory is on your `PATH`, then verify the installation:

```sh
flare-cli --help
```

`flare-cli` is the standalone CLI for Flare's API. The Flare Ruby gem has separate, project-local commands for configuring and diagnosing a Rails application:

```sh
bundle exec flare setup
bundle exec flare doctor
bundle exec flare status
```

## Authenticate

```sh
flare-cli auth login
```

The CLI opens Flare in your browser, uses a loopback callback with PKCE, and stores the resulting personal access token in your operating system credential store. If the credential store is unavailable, it warns before falling back to a mode-0600 config file.

For automation, set `FLARE_TOKEN`. To use a self-hosted or development server, set `FLARE_API_URL`.
To import an existing token without placing it in shell history or process arguments, pipe it to `flare-cli auth login --with-token`.

## Daily performance check

```sh
flare-cli org list
flare-cli project list --org 123
flare-cli environment list --project 456
flare-cli metrics overview --environment 789 --hours 24
flare-cli metrics namespace --environment 789 --namespace web --sort sum
```

Add `--json` to any data command for stable machine-readable output.

## MCP

Authenticate once, then configure every supported MCP client installed on your machine:

```sh
flare-cli auth login
flare-cli mcp install
```

The installer currently supports Codex and Claude Code. It uses the absolute path to `flare-cli`, is safe to run again, and never copies your personal access token into another application's configuration. To target one client, preview changes, or intentionally replace an existing `flare` server:

```sh
flare-cli mcp install --client codex
flare-cli mcp install --dry-run
flare-cli mcp install --client claude --force
```

For another MCP client, print a generic configuration containing the resolved executable path:

```sh
flare-cli mcp config
```

It has this shape:

```json
{
  "mcpServers": {
    "flare": {
      "command": "/absolute/path/to/flare-cli",
      "args": ["mcp"]
    }
  }
}
```

It exposes only the five curated read tools corresponding to the CLI commands. It does not expose an arbitrary metrics query or any write operation.
