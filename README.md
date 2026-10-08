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
To import an existing token without placing it in shell history or process arguments, pipe it to `flare auth login --with-token`.

## Daily performance check

```sh
flare org list
flare project list --org 123
flare environment list --project 456
flare metrics overview --environment 789 --hours 24
flare metrics namespace --environment 789 --namespace web --sort sum
flare metrics series --environment 789 --namespace job --target EventJob --hours 1
```

`metrics series` prints one row per bucket: minutes for 1h and 6h windows, hours for 24h to 168h, days for 672h. Narrow it to one operation with `--service`, `--target`, and `--operation`, using the `selector` values from `flare metrics namespace --json`.

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

It exposes only the six curated read tools corresponding to the CLI commands (including `get_metric_series` for per-minute, per-hour, or per-day series). It does not expose an arbitrary metrics query or any write operation.
