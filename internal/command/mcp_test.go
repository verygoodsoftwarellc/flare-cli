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
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

// fakeFlareTools is how many tools newFakeFlareMCPServer offers.
const fakeFlareTools = 2

// newFakeFlareMCPServer stands in for Flare's hosted MCP server.
func newFakeFlareMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "flare", Version: "test"}, nil)
	type listInput struct {
		Limit int `json:"limit,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "list_organizations", Description: "List organizations."},
		func(_ context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"organizations": []map[string]any{{"id": 1, "name": "Flare"}}, "limit": input.Limit}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "list_projects", Description: "List projects."},
		func(context.Context, *mcp.CallToolRequest, listInput) (*mcp.CallToolResult, map[string]any, error) {
			return nil, map[string]any{"projects": []any{}}, nil
		})
	return server
}

type recordedMCPRequest struct {
	httpMethod      string
	rpcMethod       string
	sessionID       string
	protocolVersion string
}

// fakeFlare serves newFakeFlareMCPServer at /mcp behind a bearer check, the
// way Flare does, and records each request.
type fakeFlare struct {
	*httptest.Server
	mutex    sync.Mutex
	requests []recordedMCPRequest
}

func newFakeFlare(t *testing.T, jsonResponse bool) *fakeFlare {
	t.Helper()
	server := newFakeFlareMCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: jsonResponse})
	fake := &fakeFlare{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/mcp" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer flare_pat_good" {
			writer.Header().Set("Content-Type", "application/problem+json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"detail":"A valid personal access token is required.","request_id":"req-1"}`))
			return
		}
		body, _ := io.ReadAll(request.Body)
		request.Body = io.NopCloser(bytes.NewReader(body))
		var envelope rpcEnvelope
		_ = json.Unmarshal(body, &envelope)
		fake.mutex.Lock()
		fake.requests = append(fake.requests, recordedMCPRequest{
			httpMethod:      request.Method,
			rpcMethod:       envelope.Method,
			sessionID:       request.Header.Get("Mcp-Session-Id"),
			protocolVersion: request.Header.Get("Mcp-Protocol-Version"),
		})
		fake.mutex.Unlock()
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(fake.Close)
	return fake
}

func testMCPProxy(baseURL, token string) *mcpProxy {
	client := api.New(baseURL, token)
	client.AuthHint = "Run `flare-cli auth login` to sign in again."
	client.ScopeHint = "Run `flare-cli auth login` to get a token with every scope."
	return newMCPProxy(client)
}

// runMCPProxy feeds input to the proxy as a client that doesn't wait for
// answers would, and returns what it wrote keyed by JSON-RPC id.
func runMCPProxy(t *testing.T, proxy *mcpProxy, input ...string) (map[string]map[string]any, string) {
	t.Helper()
	var output bytes.Buffer
	if err := proxy.run(context.Background(), strings.NewReader(strings.Join(input, "\n")+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	replies := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatalf("output line isn't one JSON message: %q", line)
		}
		id, _ := json.Marshal(reply["id"])
		replies[string(id)] = reply
	}
	return replies, output.String()
}

const (
	initializeMessage  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	initializedMessage = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	toolsListMessage   = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	toolsCallMessage   = `{"jsonrpc":"2.0","id":"call-3","method":"tools/call","params":{"name":"list_organizations","arguments":{"limit":7}}}`
)

func TestMCPProxyRelaysASessionToFlare(t *testing.T) {
	for _, test := range []struct {
		name         string
		jsonResponse bool
	}{{"event stream replies", false}, {"JSON replies", true}} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeFlare(t, test.jsonResponse)
			replies, _ := runMCPProxy(t, testMCPProxy(fake.URL, "flare_pat_good"),
				initializeMessage, initializedMessage, toolsListMessage, toolsCallMessage)

			if len(replies) != 3 {
				t.Fatalf("want answers to initialize, tools/list and tools/call only, got %v", replies)
			}
			initialized := replies["1"]["result"].(map[string]any)
			if initialized["serverInfo"].(map[string]any)["name"] != "flare" {
				t.Fatalf("initialize = %v", replies["1"])
			}
			tools := replies["2"]["result"].(map[string]any)["tools"].([]any)
			if len(tools) != fakeFlareTools {
				t.Fatalf("tools/list = %v", replies["2"])
			}
			called := replies[`"call-3"`]["result"].(map[string]any)["structuredContent"].(map[string]any)
			if called["limit"] != float64(7) || called["organizations"] == nil {
				t.Fatalf("tools/call = %v", replies[`"call-3"`])
			}

			fake.mutex.Lock()
			defer fake.mutex.Unlock()
			if len(fake.requests) != 5 || fake.requests[4].httpMethod != http.MethodDelete {
				t.Fatalf("want 4 POSTs and a closing DELETE, got %+v", fake.requests)
			}
			sessionID := fake.requests[1].sessionID
			for _, request := range fake.requests[1:] {
				if request.sessionID == "" || request.sessionID != sessionID {
					t.Fatalf("every request after initialize should carry its session: %+v", fake.requests)
				}
				if request.httpMethod == http.MethodPost && request.protocolVersion != initialized["protocolVersion"] {
					t.Fatalf("want protocol version %v on %+v", initialized["protocolVersion"], request)
				}
			}
			if fake.requests[1].rpcMethod != "notifications/initialized" {
				t.Fatalf("notifications/initialized should reach Flare before later requests: %+v", fake.requests)
			}
		})
	}
}

func TestMCPProxyWritesNothingForANotification(t *testing.T) {
	var posts atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer remote.Close()

	_, output := runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_good"), `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`)
	if output != "" || posts.Load() != 1 {
		t.Fatalf("output = %q after %d posts", output, posts.Load())
	}
}

func TestMCPProxyCompactsMultilineEventData(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, ": keepalive\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":1}}\n\n"+
			"data: {\"jsonrpc\":\"2.0\",\r\ndata:  \"id\":5,\"result\":{}}\r\n\r\n")
	}))
	defer remote.Close()

	replies, output := runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_good"), `{"jsonrpc":"2.0","id":5,"method":"ping"}`)
	if strings.Count(output, "\n") != 2 || replies["5"]["result"] == nil || replies["null"]["method"] != "notifications/progress" {
		t.Fatalf("output = %q", output)
	}
}

func TestMCPProxyAnswersARejectedTokenWithTheSignInHint(t *testing.T) {
	fake := newFakeFlare(t, false)
	replies, output := runMCPProxy(t, testMCPProxy(fake.URL, "flare_pat_expired"), initializeMessage, initializedMessage)

	if strings.Count(output, "\n") != 1 {
		t.Fatalf("only the request should get an answer, got %q", output)
	}
	rpcError, _ := replies["1"]["error"].(map[string]any)
	if rpcError["message"] != "A valid personal access token is required. Run `flare-cli auth login` to sign in again. (request req-1)" {
		t.Fatalf("error = %v", replies["1"])
	}
}

func TestMCPProxyAnswersAMissingScopeWithTheScopeHint(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"detail":"This token requires the mcp scope."}`))
	}))
	defer remote.Close()

	replies, _ := runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_old"), toolsListMessage)
	rpcError, _ := replies["2"]["error"].(map[string]any)
	if !strings.HasSuffix(rpcError["message"].(string), "Run `flare-cli auth login` to get a token with every scope.") {
		t.Fatalf("error = %v", replies["2"])
	}
}

func TestMCPProxyPassesJSONRPCErrorsThrough(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"Unknown tool"}}`))
	}))
	defer remote.Close()

	replies, _ := runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_good"), toolsListMessage)
	if replies["2"]["error"].(map[string]any)["message"] != "Unknown tool" {
		t.Fatalf("reply = %v", replies["2"])
	}
}

func TestMCPProxyAnswersARequestFlareDroppedOrCouldNotReach(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
	}))
	defer remote.Close()

	replies, _ := runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_good"), toolsListMessage)
	if replies["2"]["error"].(map[string]any)["message"] != "Flare ended the response without answering" {
		t.Fatalf("reply = %v", replies["2"])
	}

	remote.Close()
	replies, _ = runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_good"), toolsListMessage)
	if message, _ := replies["2"]["error"].(map[string]any)["message"].(string); !strings.HasPrefix(message, "request Flare:") {
		t.Fatalf("reply = %v", replies["2"])
	}
}

func TestMCPProxyDoesNotFollowRedirectsWithTheToken(t *testing.T) {
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
	}))
	defer elsewhere.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, elsewhere.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer remote.Close()

	replies, _ := runMCPProxy(t, testMCPProxy(remote.URL, "flare_pat_good"), toolsListMessage)
	if leaked.Load() {
		t.Fatal("the token followed the redirect to another host")
	}
	message, _ := replies["2"]["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "not following it with your token") {
		t.Fatalf("reply = %v", replies["2"])
	}
}

func TestMCPCommandUsesTheCLIToken(t *testing.T) {
	fake := newFakeFlare(t, true)
	t.Setenv("FLARE_TOKEN", "flare_pat_good")
	var output bytes.Buffer
	options := &rootOptions{name: "flare-cli", apiURL: fake.URL, output: &output, errors: io.Discard,
		input: strings.NewReader(initializeMessage + "\n" + toolsListMessage + "\n")}
	command := newMCPCommand(options)
	command.SetArgs([]string{})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"list_projects"`) {
		t.Fatalf("output = %q", output.String())
	}
}
