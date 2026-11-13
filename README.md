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

Without Go, releases after v0.3.0 attach prebuilt binaries for macOS, Linux, and Windows (`flare-cli_<os>_<arch>.tar.gz`, with `checksums.txt`) to the [releases page](https://github.com/verygoodsoftwarellc/flare-cli/releases). For example, on an Apple Silicon Mac:

```sh
curl -fsSL https://github.com/verygoodsoftwarellc/flare-cli/releases/latest/download/flare-cli_darwin_arm64.tar.gz | tar -xz flare-cli
sudo mv flare-cli /usr/local/bin/
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

The CLI opens Flare in your browser (and prints the URL in case it can't), uses a loopback callback with PKCE, and stores the resulting personal access token in your operating system credential store. Tokens expire, and `auth login` prints the date; run it again to get a new one. If the credential store is unavailable, it warns before falling back to a mode-0600 config file.

For automation, set `FLARE_TOKEN`. To use a self-hosted or development server, set `FLARE_API_URL`.
To import an existing token without placing it in shell history or process arguments, pipe it to `flare-cli auth login --with-token`, which checks it with Flare before saving it.

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

One command signs you in and connects every supported AI client on your machine to Flare's hosted MCP server at `https://flare.am/mcp`:

```sh
flare-cli setup
```

It finds Claude Code, Codex, Cursor and Claude Desktop, configures each one for the server (Claude Code and Codex through their own `mcp add` commands, Cursor in `~/.cursor/mcp.json`, keeping your other servers), checks the server with an MCP `initialize` and `tools/list`, and prints a first question to ask. Claude Desktop has no file for remote servers, so `setup` tells you how to add Flare as a custom connector instead.

Each client signs in to Flare itself, in your browser, the first time it connects: `/mcp` in Claude Code, `codex mcp login flare` in Codex, and Cursor's MCP settings. For a client that can't sign in on its own (a machine with no browser, say), `--token` writes your personal access token into its configuration instead. Codex can't store a header from its CLI, so with `--token` it reads the token from `FLARE_TOKEN`.

```sh
flare-cli setup --dry-run
flare-cli setup --client cursor
flare-cli setup --force
flare-cli setup --token
```

`setup` leaves a client's existing `flare` server alone unless it already matches; `--force` replaces it. `flare-cli mcp install --remote --client <name>` does the same as `setup` for chosen clients.

### Local MCP server

`flare-cli mcp` runs the same tools as a local stdio server that calls Flare's read API with your personal access token, for clients or machines where the hosted server doesn't fit. Configure it with:

```sh
flare-cli auth login
flare-cli mcp install
```

The local installer supports Codex and Claude Code. It uses the absolute path to `flare-cli`, is safe to run again, and never copies your personal access token into another application's configuration. To target one client, preview changes, or intentionally replace an existing `flare` server:

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

It exposes the same eleven read tools as the hosted server: the three catalog tools, the presets `get_environment_overviews`, `get_namespace_overview`, `list_namespace_operations`, and `get_operation_series`, the generic `query_metrics`, deploys with `list_changes`, and captured traces with `list_traces` and `get_trace`. It does not expose any write operation. The trace tools need the `traces:read` scope, which `auth login` grants; a token from before it existed gets a 403 naming the scope, and signing in again fixes it.
