# Flare CLI

Read application performance data from [Flare](https://flare.am).

## Install

```sh
go install github.com/verygoodsoftwarellc/flare-cli@latest
```

## Authenticate

```sh
flare auth login
```

The CLI opens Flare in your browser, uses a loopback callback with PKCE, and stores the resulting personal access token in your operating system credential store. If the credential store is unavailable, it warns before falling back to a mode-0600 config file.

For automation, set `FLARE_TOKEN`. To use a self-hosted or development server, set `FLARE_API_URL`.

## Daily performance check

```sh
flare org list
flare project list --org 123
flare environment list --project 456
flare metrics overview --environment 789 --hours 24
flare metrics namespace --environment 789 --namespace web --sort sum
```

Add `--json` to any data command for stable machine-readable output.

## MCP

The same binary includes a read-only MCP server over stdio:

```json
{
  "mcpServers": {
    "flare": {
      "command": "flare",
      "args": ["mcp"]
    }
  }
}
```

It exposes only the five curated read tools corresponding to the CLI commands. It does not expose an arbitrary metrics query or any write operation.
