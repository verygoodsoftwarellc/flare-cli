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

The CLI opens Flare in your browser (and prints the URL in case it can't), uses a loopback callback with PKCE, and stores the resulting personal access token in your operating system credential store. If the credential store is unavailable, it warns before falling back to a mode-0600 config file.

For automation, set `FLARE_TOKEN`. To use a self-hosted or development server, set `FLARE_API_URL`.
To import an existing token without placing it in shell history or process arguments, pipe it to `flare-cli auth login --with-token`.

## Daily performance check

```sh
flare-cli org list
flare-cli project list --org 123
flare-cli environment list --project 456
flare-cli metrics overview --environment 789 --hours 24
flare-cli metrics namespace --environment 789 --namespace web --sort sum
flare-cli metrics namespace-overview --environment 789 --namespace job --hours 1
flare-cli metrics series --environment 789 --namespace job --target EventJob --hours 1
flare-cli metrics query --environment 789 --namespace web --target UsersController#index --group-by operation --sort count
```

The metrics commands mirror the Flare web pages. `overview` is the environment page (per-namespace stats and top operations). `namespace-overview` is the namespace page: stats, a time series, and the top operations by sum and by count, each with its own series. `namespace` is the paginated operations table; when there are more rows it prints the `--cursor` to pass for the next page. `series` is the operation page: stats and one row per bucket for one operation, selected with `--service`, `--target`, and `--operation` from the `selector` values in `namespace --json`.

`metrics query` is the generic form the others are presets of: filter on any of namespace, service, target, and operation, group by any subset of them with `--group-by`, rank with `--sort` and `--direction` (add `--min-count` for rate sorts), and attach series with `--series` (JSON output).

Series use minute buckets for 1h and 6h windows, hour buckets for 24h to 168h, and day buckets for 672h. In JSON every series is `{count, sum, error_count}` arrays aligned to the response's `buckets` list; per-bucket averages are `sum / count`.

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

It exposes only the eight curated read tools corresponding to the CLI commands: the three catalog tools, the presets `get_environment_overviews`, `get_namespace_overview`, `list_namespace_operations`, and `get_operation_series`, and the generic `query_metrics`. It does not expose any write operation.
