package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/version"
)

func TestMCPInitializesListsAndCallsCuratedReadTools(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/organizations" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"organizations":[{"id":1,"name":"Flare"}],"pagination":{"next":null,"previous":null}}`))
	}))
	defer apiServer.Close()

	session := connectMCPClient(t, newMCPServer(api.New(apiServer.URL, "flare_pat_test")))
	initialized := session.InitializeResult()
	if initialized.ServerInfo.Name != "flare" || initialized.ServerInfo.Version != version.Current() {
		t.Fatalf("unexpected server info %#v", initialized.ServerInfo)
	}

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 8 {
		t.Fatalf("expected 8 curated tools, got %d", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s is not marked read-only", tool.Name)
		}
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_organizations", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error %#v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["organizations"] == nil {
		t.Fatalf("unexpected result %#v", result.StructuredContent)
	}
}

func TestMCPToolsCallOnlyExpectedReadEndpoints(t *testing.T) {
	type expectedRequest struct {
		path     string
		query    string
		response string
	}
	expected := []expectedRequest{
		{path: "/api/v1/organizations", query: "cursor=next-org&limit=10", response: `{"organizations":[],"pagination":{"next":null,"previous":null}}`},
		{path: "/api/v1/projects", query: "limit=100&organization_id=4", response: `{"projects":[],"pagination":{"next":null,"previous":null}}`},
		{path: "/api/v1/environments", query: "limit=100&project_id=9", response: `{"environments":[],"pagination":{"next":null,"previous":null}}`},
		{path: "/api/v1/overviews", query: "environment_ids=18%2C19&hours=24&limit=5&sort=sum", response: `{"overviews":[]}`},
		{path: "/api/v1/namespaces", query: "direction=desc&environment_id=18&hours=24&limit=25&namespace=web&sort=sum", response: `{"environment":{"id":18,"project_id":9,"name":"Production"},"namespace":"web","operations":[],"pagination":{"next":null,"previous":null}}`},
		{path: "/api/v1/namespaces/overview", query: "environment_id=18&hours=1&limit=10&namespace=job", response: `{"environment":{"id":18,"project_id":9,"name":"Production"},"namespace":"job","hours":1,"bucket":"minute","as_of":"2026-10-07T20:31:50Z","window_start":"2026-10-07T19:31:00Z","window_end":"2026-10-07T20:31:50Z","buckets":[],"data_through":null,"stats":{"count":0,"sum":0,"avg":0,"error_count":0,"error_rate":0,"impact":0},"series":{"count":[],"sum":[],"error_count":[]},"top_by_sum":[],"top_by_count":[]}`},
		{path: "/api/v1/metrics/query", query: "direction=desc&environment_id=18&group_by=operation&hours=24&limit=10&min_count=5&namespace=web&series=true&sort=count&target=UsersController%23index", response: `{"environment":{"id":18,"project_id":9,"name":"Production"},"hours":24,"bucket":"hour","as_of":"2026-10-07T20:31:50Z","window_start":"2026-10-06T20:00:00Z","window_end":"2026-10-07T20:31:50Z","buckets":[],"filter":{"namespace":"web","target":"UsersController#index"},"group_by":["operation"],"sort":"count","direction":"desc","limit":10,"min_count":5,"data_through":null,"total":{"count":0,"sum":0,"avg":0,"error_count":0,"error_rate":0},"groups":[]}`},
		{path: "/api/v1/series", query: "environment_id=18&hours=24&namespace=job&target=EventJob", response: `{"environment":{"id":18,"project_id":9,"name":"Production"},"namespace":"job","selector":{"target":"EventJob"},"hours":24,"bucket":"hour","as_of":"2026-10-07T20:31:50Z","window_start":"2026-10-06T20:00:00Z","window_end":"2026-10-07T20:31:50Z","buckets":[],"data_through":null,"stats":{"count":0,"sum":0,"avg":0,"error_count":0,"error_rate":0},"series":{"count":[],"sum":[],"error_count":[]}}`},
	}
	requestIndex := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requestIndex >= len(expected) {
			t.Fatalf("unexpected extra request %s", request.URL.String())
		}
		want := expected[requestIndex]
		requestIndex++
		if request.Method != http.MethodGet || request.URL.Path != want.path || request.URL.RawQuery != want.query {
			t.Fatalf("unexpected request %s %s, want GET %s?%s", request.Method, request.URL.String(), want.path, want.query)
		}
		_, _ = writer.Write([]byte(want.response))
	}))
	defer apiServer.Close()
	session := connectMCPClient(t, newMCPServer(api.New(apiServer.URL, "flare_pat_test")))

	calls := []*mcp.CallToolParams{
		{Name: "list_organizations", Arguments: map[string]any{"cursor": "next-org", "limit": 10}},
		{Name: "list_projects", Arguments: map[string]any{"organization_id": 4}},
		{Name: "list_environments", Arguments: map[string]any{"project_id": 9}},
		{Name: "get_environment_overviews", Arguments: map[string]any{"environment_ids": []int{18, 19}}},
		{Name: "list_namespace_operations", Arguments: map[string]any{"environment_id": 18, "namespace": "web"}},
		{Name: "get_namespace_overview", Arguments: map[string]any{"environment_id": 18, "namespace": "job", "hours": 1}},
		{Name: "query_metrics", Arguments: map[string]any{"environment_id": 18, "namespace": "web", "target": "UsersController#index", "group_by": []string{"operation"}, "sort": "count", "min_count": 5, "series": true}},
		{Name: "get_operation_series", Arguments: map[string]any{"environment_id": 18, "namespace": "job", "target": "EventJob"}},
	}
	for _, call := range calls {
		result, err := session.CallTool(context.Background(), call)
		if err != nil || result.IsError {
			text := ""
			if result != nil && len(result.Content) > 0 {
				if content, ok := result.Content[0].(*mcp.TextContent); ok {
					text = content.Text
				}
			}
			t.Fatalf("call %s failed: %s err=%v", call.Name, text, err)
		}
	}
	if requestIndex != len(expected) {
		t.Fatalf("got %d API requests, want %d", requestIndex, len(expected))
	}
}

func TestMCPRejectsInvalidArgumentsAndUnknownTools(t *testing.T) {
	session := connectMCPClient(t, newMCPServer(api.New("http://127.0.0.1:1", "flare_pat_test")))
	tests := []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{name: "fractional ID", tool: "list_namespace_operations", arguments: map[string]any{"environment_id": 12.9, "namespace": "web"}},
		{name: "unknown argument", tool: "list_namespace_operations", arguments: map[string]any{"environment_id": 12, "namespace": "web", "unexpected": true}},
		{name: "zero organization", tool: "list_projects", arguments: map[string]any{"organization_id": 0}},
		{name: "duplicate environments", tool: "get_environment_overviews", arguments: map[string]any{"environment_ids": []int{1, 1}}},
		{name: "unsupported hours", tool: "get_operation_series", arguments: map[string]any{"environment_id": 11, "namespace": "job", "hours": 2}},
		{name: "unknown namespace", tool: "get_operation_series", arguments: map[string]any{"environment_id": 11, "namespace": "nope"}},
		{name: "overview limit above series maximum", tool: "get_namespace_overview", arguments: map[string]any{"environment_id": 11, "namespace": "job", "limit": 26}},
		{name: "unknown group_by", tool: "query_metrics", arguments: map[string]any{"environment_id": 11, "group_by": []string{"color"}}},
		{name: "series limit above maximum", tool: "query_metrics", arguments: map[string]any{"environment_id": 11, "series": true, "limit": 26}},
		{name: "impact is not a query sort", tool: "query_metrics", arguments: map[string]any{"environment_id": 11, "sort": "impact"}},
		{name: "repeated group_by", tool: "query_metrics", arguments: map[string]any{"environment_id": 11, "group_by": []string{"target", "target"}}},
		{name: "unknown tool", tool: "missing_tool", arguments: map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: test.tool, Arguments: test.arguments})
			if err == nil && (result == nil || !result.IsError) {
				t.Fatalf("expected rejection, got result=%#v err=%v", result, err)
			}
		})
	}
}

func TestMCPQueryLengthCountsUnicodeCharacters(t *testing.T) {
	requests := make(chan *http.Request, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request
		_, _ = writer.Write([]byte(`{"environment":{"id":1,"project_id":1,"name":"Production"},"namespace":"web","operations":[],"pagination":{"next":null,"previous":null}}`))
	}))
	defer apiServer.Close()
	session := connectMCPClient(t, newMCPServer(api.New(apiServer.URL, "flare_pat_test")))

	query := strings.Repeat("🔥", 200)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_namespace_operations",
		Arguments: map[string]any{
			"environment_id": 1,
			"namespace":      "web",
			"q":              query,
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("expected 200 Unicode characters to be accepted, result=%#v err=%v", result, err)
	}
	request := <-requests
	if request.URL.Query().Get("q") != query {
		t.Fatal("Unicode query was not forwarded intact")
	}

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_namespace_operations",
		Arguments: map[string]any{
			"environment_id": 1,
			"namespace":      "web",
			"q":              strings.Repeat("🔥", 256),
		},
	})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("expected 256 Unicode characters to be rejected, result=%#v err=%v", result, err)
	}
}

func TestMCPCancellationReachesFlareRequest(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	apiServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(cancelled)
	}))
	defer apiServer.Close()
	session := connectMCPClient(t, newMCPServer(api.New(apiServer.URL, "flare_pat_test")))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "list_organizations", Arguments: map[string]any{}})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Flare request did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("MCP cancellation did not reach Flare request")
	}
	<-done
}

func connectMCPClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "flare-cli-test", Version: "dev"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		cancel()
	})
	return clientSession
}
