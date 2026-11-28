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

## Tools

The CLI runs the tools on Flare's hosted MCP server, the same ones your AI clients see. It offers whatever tools the server has for your token, so a new tool works here without a new CLI release.

```sh
flare-cli tools
flare-cli tools query_metrics
```

`tools` lists each tool with the first sentence of its description. `tools <name>` shows the whole description and its arguments, with their types, which are required, and the values they take. Add `--json` for the tools as the server lists them, input schemas included.

`call` runs a tool. Pass arguments as `key=value`: a value that parses as JSON (`42`, `true`, `[789]`) is sent as JSON, and anything else as a string.

```sh
flare-cli call list_environments project_id=456
flare-cli call get_environment_overviews environment_ids='[789]' hours=24
flare-cli call query_metrics environment_id=789 namespace=web group_by='["target"]' sort=avg limit=10
```

For many arguments, pass a JSON object with `--json-args`, or `--json-args -` to read it from stdin. `key=value` pairs replace its keys.

```sh
echo '{"environment_id":789,"namespace":"job","group_by":["target"],"sort":"error_rate","min_count":10}' | flare-cli call query_metrics --json-args -
```

`call` prints the tool's text, with JSON indented. `--json` prints the tool's structured content, or its content list when it has none. When a tool fails, `call` prints its message on stderr and exits non-zero.

`call` replaces the old `org`, `project`, `environment` and `metrics` commands: `org list` is `call list_organizations`, `metrics overview` is `call get_environment_overviews`, `metrics query` is `call query_metrics`, and so on. List tools return one page at a time; pass the response's `next` cursor back as `cursor=...` for the next one.

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

`flare-cli mcp` is a stdio MCP server for clients that only run local servers. It forwards each message to Flare's hosted MCP server with your personal access token (from `auth login` or `FLARE_TOKEN`, honoring `--api-url`) and writes the replies back, so it always offers the hosted server's tools. Configure it with:

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

It offers whatever the hosted server does. Tools that change something are listed only when your token has their scope. Changing alert settings needs `alerts:write`, which `auth login` offers as an unticked box in the browser: run it again and tick the box to add it. If your token is missing a scope a tool needs, the tool's error says so.
