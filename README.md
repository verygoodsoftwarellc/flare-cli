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
flare metrics namespace-overview --environment 789 --namespace job --hours 1
flare metrics series --environment 789 --namespace job --target EventJob --hours 1
```

The metrics commands mirror the Flare web pages. `overview` is the environment page (per-namespace stats and top operations). `namespace-overview` is the namespace page: stats, a time series, and the top operations by sum and by count, each with its own series. `namespace` is the paginated operations table. `series` is the operation page: stats and one row per bucket for one operation, selected with `--service`, `--target`, and `--operation` from the `selector` values in `namespace --json`.

Series use minute buckets for 1h and 6h windows, hour buckets for 24h to 168h, and day buckets for 672h. In JSON every series is `{count, sum, error_count}` arrays aligned to the response's `buckets` list; per-bucket averages are `sum / count`.

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

It exposes only the seven curated read tools corresponding to the CLI commands: the three catalog tools plus `get_environment_overviews`, `get_namespace_overview`, `list_namespace_operations`, and `get_operation_series`. It does not expose an arbitrary metrics query or any write operation.
