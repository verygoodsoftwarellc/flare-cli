package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// railsLikeFlare stands in for Flare's /mcp the way the Rails app serves it:
// stateless, JSON replies, a bearer token on every request, and tools whose
// result is the API's JSON as text. It records what each call received.
type railsLikeFlare struct {
	*httptest.Server
	mutex          sync.Mutex
	authorizations []string
	arguments      map[string]any
}

func newRailsLikeFlare(t *testing.T) *railsLikeFlare {
	t.Helper()
	fake := &railsLikeFlare{}
	// One tool per page, so listing them has to follow nextCursor.
	server := mcp.NewServer(&mcp.Implementation{Name: "flare", Version: "1.0.0"}, &mcp.ServerOptions{PageSize: 1})
	record := func(request *mcp.CallToolRequest) {
		fake.mutex.Lock()
		defer fake.mutex.Unlock()
		fake.arguments = map[string]any{}
		_ = json.Unmarshal(request.Params.Arguments, &fake.arguments)
	}
	text := func(text string, isError bool) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
	}
	server.AddTool(&mcp.Tool{
		Name:        "list_organizations",
		Description: "List accessible Flare organizations. Each has an id and a name.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100,"default":25}},"additionalProperties":false}`),
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		record(request)
		return text(`{"organizations":[{"id":1,"name":"Flare"}],"pagination":{"next":null}}`, false), nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "query_metrics",
		Description: "Generic Flare metrics query\x1b[31m; the other metrics tools are presets of it. Filter on any of the four dimensions.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"environment_id":{"type":"integer","minimum":1},` +
			`"hours":{"type":"integer","enum":[1,6,24,168,672],"default":24},` +
			`"group_by":{"type":"array","items":{"type":"string","enum":["service","target"]},"maxItems":2,"uniqueItems":true},` +
			`"target":{"type":"string","maxLength":500}` +
			`},"required":["environment_id"],"additionalProperties":false}`),
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		record(request)
		result := text(`{"groups":[]}`, false)
		result.StructuredContent = map[string]any{"groups": []any{}}
		return result, nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "list_alerts",
		Description: "List a Flare environment's alerts, newest first.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"environment_id":{"type":"integer"}},"required":["environment_id"]}`),
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		record(request)
		return text("Flare API returned 404: \x1b[2J\u202eEnvironment not found.", true), nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fake.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.mutex.Lock()
		fake.authorizations = append(fake.authorizations, request.Header.Get("Authorization"))
		fake.mutex.Unlock()
		if request.URL.Path != "/mcp" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer flare_pat_good" {
			writer.Header().Set("Content-Type", "application/problem+json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"title":"Unauthorized","detail":"A valid personal access token is required."}`))
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(fake.Close)
	return fake
}

// runToolCommand runs `tools` or `call` against the fake with the CLI's
// token from FLARE_TOKEN.
func runToolCommand(t *testing.T, fake *railsLikeFlare, jsonOutput bool, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("FLARE_TOKEN", "flare_pat_good")
	var output bytes.Buffer
	options := &rootOptions{name: "flare-cli", apiURL: fake.URL, json: jsonOutput, output: &output, errors: io.Discard, input: strings.NewReader(stdin)}
	command := newToolsCommand(options)
	if args[0] == "call" {
		command = newCallCommand(options)
	}
	command.SetArgs(args[1:])
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	err := command.ExecuteContext(context.Background())
	return output.String(), err
}

func TestToolArgumentsParseKeyValuePairs(t *testing.T) {
	arguments, err := toolArguments("", nil, []string{
		"limit=7",
		"series=true",
		"environment_ids=[1, 2]",
		"target=UsersController#index",
		`quoted="true"`,
		"empty=",
		"big=12345678901234567890",
		"filter=a=b",
		"nothing=null",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"limit":           `7`,
		"series":          `true`,
		"environment_ids": `[1, 2]`,
		"target":          `"UsersController#index"`,
		"quoted":          `"true"`,
		"empty":           `""`,
		"big":             `12345678901234567890`,
		"filter":          `"a=b"`,
		"nothing":         `null`,
	}
	if len(arguments) != len(want) {
		t.Fatalf("arguments = %s", arguments)
	}
	for key, value := range want {
		if string(arguments[key]) != value {
			t.Errorf("%s = %s, want %s", key, arguments[key], value)
		}
	}

	for _, bad := range []string{"limit", "=7"} {
		if _, err := toolArguments("", nil, []string{bad}); err == nil || !strings.Contains(err.Error(), "isn't key=value") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestToolArgumentsFromJSONArgs(t *testing.T) {
	arguments, err := toolArguments(`{"environment_id":789,"hours":24}`, nil, []string{"hours=1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments["environment_id"]) != "789" || string(arguments["hours"]) != "1" {
		t.Fatalf("key=value should replace --json-args keys: %s", arguments)
	}

	arguments, err = toolArguments("-", strings.NewReader(`{"group_by":["target"]}`+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments["group_by"]) != `["target"]` {
		t.Fatalf("arguments from stdin = %s", arguments)
	}

	for _, bad := range []string{`[1]`, `null`, `nope`, `"text"`} {
		if _, err := toolArguments(bad, nil, nil); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
			t.Errorf("%s: err = %v", bad, err)
		}
		if _, err := toolArguments("-", strings.NewReader(bad), nil); err == nil {
			t.Errorf("%s on stdin: expected an error", bad)
		}
	}
}

func TestToolSummaryIsTheDescriptionsLead(t *testing.T) {
	for description, want := range map[string]string{
		"List Flare organizations the user can access.":                                 "List Flare organizations the user can access.",
		"Get bulk summaries for up to 25 environments. Fields: count = requests":        "Get bulk summaries for up to 25 environments.",
		"List traces, newest first: root_name, started_at. Then more.":                  "List traces, newest first.",
		"Generic Flare metrics query; the other metrics tools are presets of it. More.": "Generic Flare metrics query; the other metrics tools are presets of it.",
		"Get one rule and its traces (each with reason: slow or error). Then more.":     "Get one rule and its traces (each with reason: slow or error).",
		"First line\nsecond line": "First line",
		"":                        "",
	} {
		if got := summary(description); got != want {
			t.Errorf("summary(%q) = %q, want %q", description, got, want)
		}
	}
}

func TestToolsListsEveryHostedTool(t *testing.T) {
	fake := newRailsLikeFlare(t)
	output, err := runToolCommand(t, fake, false, "", "tools")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("want a header and all three tools across pages:\n%s", output)
	}
	if !strings.Contains(output, "List accessible Flare organizations.\n") {
		t.Fatalf("want each description's first sentence:\n%s", output)
	}
	if strings.Contains(output, "\x1b") || !strings.Contains(output, "Generic Flare metrics query[31m; the other metrics tools are presets of it.") {
		t.Fatalf("control characters from the server should be dropped:\n%q", output)
	}

	output, err = runToolCommand(t, fake, true, "", "tools")
	if err != nil {
		t.Fatal(err)
	}
	var tools []map[string]any
	if err := json.Unmarshal([]byte(output), &tools); err != nil || len(tools) != 3 || tools[0]["inputSchema"] == nil {
		t.Fatalf("--json should print the raw tool list (%v):\n%s", err, output)
	}
}

func TestToolsShowsOneToolsArguments(t *testing.T) {
	fake := newRailsLikeFlare(t)
	output, err := runToolCommand(t, fake, false, "", "tools", "query_metrics")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"query_metrics\n\nGeneric Flare metrics query",
		"Filter on any of the four dimensions.\n",
		"environment_id  integer          yes       at least 1\n",
		"group_by        array of string  no        each one of service, target; at most 2 items\n",
		"hours           integer          no        one of 1, 6, 24, 168, 672; default 24\n",
		"target          string           no        up to 500 characters\n",
		"Run it with:\n  flare-cli call query_metrics environment_id=<integer>\n",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b") {
		t.Errorf("control characters from the server should be dropped: %q", output)
	}

	output, err = runToolCommand(t, fake, true, "", "tools", "list_organizations")
	if err != nil || !strings.Contains(output, `"name": "list_organizations"`) || !strings.Contains(output, `"inputSchema"`) {
		t.Fatalf("--json should print the tool as listed (%v):\n%s", err, output)
	}

	if _, err := runToolCommand(t, fake, false, "", "tools", "list_nothing"); err == nil || !strings.Contains(err.Error(), "run `flare-cli tools` to list them") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallRunsAHostedToolWithTheCLIToken(t *testing.T) {
	fake := newRailsLikeFlare(t)
	output, err := runToolCommand(t, fake, false, "", "call", "list_organizations", "limit=7")
	if err != nil {
		t.Fatal(err)
	}
	if output != "{\n  \"organizations\": [\n    {\n      \"id\": 1,\n      \"name\": \"Flare\"\n    }\n  ],\n  \"pagination\": {\n    \"next\": null\n  }\n}\n" {
		t.Fatalf("want the tool's JSON text, indented:\n%s", output)
	}
	fake.mutex.Lock()
	if fake.arguments["limit"] != float64(7) || len(fake.arguments) != 1 {
		t.Errorf("tool received %v", fake.arguments)
	}
	for _, authorization := range fake.authorizations {
		if authorization != "Bearer flare_pat_good" {
			t.Errorf("every request should carry the CLI token, got %q", authorization)
		}
	}
	fake.mutex.Unlock()

	output, err = runToolCommand(t, fake, true, "", "call", "list_organizations")
	var content []map[string]any
	if err != nil || json.Unmarshal([]byte(output), &content) != nil || len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("--json without structured content should print the content list (%v):\n%s", err, output)
	}

	output, err = runToolCommand(t, fake, true, `{"environment_id":789,"group_by":["target"]}`, "call", "query_metrics", "--json-args", "-")
	if err != nil || strings.TrimSpace(output) != "{\n  \"groups\": []\n}" {
		t.Fatalf("--json should print structured content (%v):\n%s", err, output)
	}
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if fake.arguments["environment_id"] != float64(789) || fake.arguments["group_by"].([]any)[0] != "target" {
		t.Errorf("tool received %v", fake.arguments)
	}
}

func TestCallFailsWhenTheToolDoes(t *testing.T) {
	fake := newRailsLikeFlare(t)
	output, err := runToolCommand(t, fake, false, "", "call", "list_alerts", "environment_id=999")
	if err == nil || err.Error() != "list_alerts: Flare API returned 404: [2JEnvironment not found." || output != "" {
		t.Fatalf("a result with isError should fail with its text: err = %q, output = %q", err, output)
	}

	_, err = runToolCommand(t, fake, false, "", "call", "list_nothing")
	if err == nil || !strings.Contains(err.Error(), "list_nothing") {
		t.Fatalf("a JSON-RPC error should fail with its message: %v", err)
	}
	railsError := &rpcError{Code: -32602, Message: "Invalid params", Data: json.RawMessage(`"Tool not found: \u001b[2Jnope"`)}
	if railsError.Error() != "Invalid params: Tool not found: [2Jnope" {
		t.Fatalf("want the message, then its data without control characters: %q", railsError.Error())
	}

	_, err = runToolCommand(t, fake, false, "", "call", "list_organizations", "oops")
	if err == nil || !strings.Contains(err.Error(), `argument "oops" isn't key=value`) {
		t.Fatalf("err = %v", err)
	}
}

func TestToolsAndCallExplainARejectedToken(t *testing.T) {
	fake := newRailsLikeFlare(t)
	for _, args := range [][]string{{"tools"}, {"call", "list_organizations"}} {
		t.Setenv("FLARE_TOKEN", "flare_pat_expired")
		var output bytes.Buffer
		options := &rootOptions{name: "flare-cli", apiURL: fake.URL, output: &output, errors: io.Discard}
		root := newToolsCommand(options)
		if args[0] == "call" {
			root = newCallCommand(options)
		}
		root.SetArgs(args[1:])
		err := root.ExecuteContext(context.Background())
		want := "A valid personal access token is required. FLARE_TOKEN was rejected; replace it, or unset it and run `flare-cli auth login` with the same --api-url."
		if err == nil || err.Error() != want {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if len(fake.authorizations) != 2 || fake.authorizations[0] != "Bearer flare_pat_expired" {
		t.Fatalf("authorizations = %q", fake.authorizations)
	}
}

func TestToolTextEscapesInvisibleCharactersInJSON(t *testing.T) {
	result := toolResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"{\"name\":\"evil` + "\u202e" + `exe.txt\",\"emoji\":\"🔥\"}"}`)}}
	output := toolText(result)
	if strings.ContainsRune(output, '\u202e') || !strings.Contains(output, `"name": "evil\u202eexe.txt"`) || !strings.Contains(output, "🔥") {
		t.Fatalf("want the direction override escaped and visible characters kept:\n%q", output)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(output), &decoded); err != nil || decoded["name"] != "evil\u202eexe.txt" {
		t.Fatalf("escaped JSON should decode to the same value: %v %q", err, decoded["name"])
	}
}

func TestToolsKeepsAToolWhoseSchemaDoesNotDecode(t *testing.T) {
	odd := json.RawMessage(`{"name":"odd_tool","description":"Has a boolean items schema.","inputSchema":{"type":"object","properties":{"ids":{"type":"array","items":false}}}}`)
	var table bytes.Buffer
	if err := writeToolTable(&table, []json.RawMessage{odd}); err != nil || !strings.Contains(table.String(), "odd_tool") {
		t.Fatalf("want the tool listed even when its schema doesn't decode: %v\n%s", err, table.String())
	}
	var help bytes.Buffer
	if err := writeToolHelp(&help, "flare-cli", hostedTool{Name: "odd_tool", Description: "Has a boolean items schema."}, true); err != nil ||
		!strings.Contains(help.String(), "Run `flare-cli tools odd_tool --json` to see its arguments.") {
		t.Fatalf("want a pointer to --json for an unreadable schema: %v\n%s", err, help.String())
	}
}

func TestHostedMCPPrintsRateLimitsWhenVerbose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("RateLimit", "limit=600, remaining=599, reset=60")
		writer.Header().Set("X-Request-ID", "req-1\u202e")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer server.Close()
	var verbose bytes.Buffer
	hosted := newHostedMCPAt(server.URL, "flare_pat_good")
	hosted.verbose, hosted.errWriter = true, &verbose
	if _, err := hosted.listTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(verbose.String(), "rate-limit: limit=600, remaining=599, reset=60 policy= request=req-1\n") {
		t.Fatalf("want the rate limit and request id: %q", verbose.String())
	}
}

func TestToolTextKeepsJSONValidWithATrailingCRLF(t *testing.T) {
	result := toolResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"{\"ok\":true}\r\n"}`)}}
	output := toolText(result)
	var decoded map[string]bool
	if err := json.Unmarshal([]byte(output), &decoded); err != nil || !decoded["ok"] {
		t.Fatalf("want valid JSON after a trailing CRLF: %v %q", err, output)
	}
}
