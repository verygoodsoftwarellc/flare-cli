package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

func TestMCPListsAndCallsCuratedReadTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/organizations" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"organizations":[{"id":1,"name":"Flare"}],"pagination":{"next":null,"previous":null}}`))
	}))
	defer server.Close()

	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_organizations","arguments":{}}}` + "\n",
	)
	var output bytes.Buffer
	if err := serveMCP(context.Background(), api.New(server.URL, "flare_pat_test"), input, &output); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(&output)
	var listResponse, callResponse map[string]any
	if err := decoder.Decode(&listResponse); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&callResponse); err != nil {
		t.Fatal(err)
	}
	tools := listResponse["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 8 {
		t.Fatalf("expected 8 curated tools, got %d", len(tools))
	}
	structured := callResponse["result"].(map[string]any)["structuredContent"].(map[string]any)
	if _, ok := structured["organizations"]; !ok {
		t.Fatalf("unexpected result %#v", structured)
	}
}

func TestMCPOperationSeriesToolForwardsSelector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/series" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("environment_id") != "11" || query.Get("namespace") != "job" || query.Get("hours") != "24" ||
			query.Get("target") != "EventJob" || query.Has("service") || query.Has("operation") {
			t.Fatalf("unexpected query %s", request.URL.RawQuery)
		}
		_, _ = writer.Write([]byte(`{"environment":{"id":11,"project_id":1,"name":"production"},"namespace":"job","selector":{"target":"EventJob"},"hours":24,"bucket":"hour","as_of":"2026-10-07T20:31:50Z","window_start":"2026-10-06T20:00:00Z","window_end":"2026-10-07T20:31:50Z","buckets":["2026-10-06T20:00:00Z"],"data_through":"2026-10-06T20:00:00Z","stats":{"count":12,"sum":340,"avg":28.33,"error_count":0,"error_rate":0},"series":{"count":[12],"sum":[340],"error_count":[0]}}`))
	}))
	defer server.Close()

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_operation_series","arguments":{"environment_id":11,"namespace":"job","target":"EventJob"}}}` + "\n")
	var output bytes.Buffer
	if err := serveMCP(context.Background(), api.New(server.URL, "flare_pat_test"), input, &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	structured := response["result"].(map[string]any)["structuredContent"].(map[string]any)
	if structured["bucket"] != "hour" || len(structured["series"].(map[string]any)["count"].([]any)) != 1 {
		t.Fatalf("unexpected result %#v", structured)
	}
}

func TestMCPNamespaceOverviewToolForwardsArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/namespaces/overview" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("environment_id") != "11" || query.Get("namespace") != "job" || query.Get("hours") != "1" || query.Get("limit") != "10" {
			t.Fatalf("unexpected query %s", request.URL.RawQuery)
		}
		_, _ = writer.Write([]byte(`{"environment":{"id":11,"project_id":1,"name":"production"},"namespace":"job","hours":1,"bucket":"minute","as_of":"2026-10-07T20:31:50Z","window_start":"2026-10-07T19:31:00Z","window_end":"2026-10-07T20:31:50Z","buckets":["2026-10-07T19:31:00Z"],"data_through":"2026-10-07T19:31:00Z","stats":{"count":12,"sum":340,"avg":28.33,"error_count":0,"error_rate":0,"impact":73.5},"series":{"count":[12],"sum":[340],"error_count":[0]},"top_by_sum":[{"operation":"Event","selector":{"service":"solid_queue","target":"EventJob","operation":"perform"},"count":12,"sum":340,"avg":28.33,"error_count":0,"error_rate":0,"impact":100,"series":{"count":[12],"sum":[340],"error_count":[0]}}],"top_by_count":[]}`))
	}))
	defer server.Close()

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_namespace_overview","arguments":{"environment_id":11,"namespace":"job","hours":1}}}` + "\n")
	var output bytes.Buffer
	if err := serveMCP(context.Background(), api.New(server.URL, "flare_pat_test"), input, &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	structured := response["result"].(map[string]any)["structuredContent"].(map[string]any)
	if len(structured["top_by_sum"].([]any)) != 1 || len(structured["buckets"].([]any)) != 1 {
		t.Fatalf("unexpected result %#v", structured)
	}
}

func TestMCPQueryToolForwardsArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/metrics/query" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		query := request.URL.Query()
		if query.Get("environment_id") != "11" || query.Get("namespace") != "web" || query.Get("target") != "UsersController#index" ||
			query.Get("group_by") != "operation" || query.Get("sort") != "count" || query.Get("min_count") != "5" || query.Get("series") != "true" ||
			query.Get("hours") != "1" || query.Get("limit") != "5" {
			t.Fatalf("unexpected query %s", request.URL.RawQuery)
		}
		_, _ = writer.Write([]byte(`{"environment":{"id":11,"project_id":1,"name":"production"},"hours":1,"bucket":"minute","as_of":"2026-10-07T20:31:50Z","window_start":"2026-10-07T19:31:00Z","window_end":"2026-10-07T20:31:50Z","buckets":["2026-10-07T19:31:00Z"],"filter":{"namespace":"web","target":"UsersController#index"},"group_by":["operation"],"sort":"count","direction":"desc","limit":5,"min_count":5,"data_through":"2026-10-07T19:31:00Z","total":{"count":12,"sum":340,"avg":28.33,"error_count":1,"error_rate":8.33,"series":{"count":[12],"sum":[340],"error_count":[1]}},"groups":[{"selector":{"operation":"2xx"},"label":"Users#index","count":11,"sum":300,"avg":27.27,"error_count":0,"error_rate":0,"impact":88.2,"series":{"count":[11],"sum":[300],"error_count":[0]}}]}`))
	}))
	defer server.Close()

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_metrics","arguments":{"environment_id":11,"namespace":"web","target":"UsersController#index","group_by":["operation"],"sort":"count","min_count":5,"series":true,"hours":1,"limit":5}}}` + "\n")
	var output bytes.Buffer
	if err := serveMCP(context.Background(), api.New(server.URL, "flare_pat_test"), input, &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	structured := response["result"].(map[string]any)["structuredContent"].(map[string]any)
	if len(structured["groups"].([]any)) != 1 || structured["total"].(map[string]any)["count"] != float64(12) {
		t.Fatalf("unexpected result %#v", structured)
	}
}

func TestMCPSeriesToolsRejectInvalidArguments(t *testing.T) {
	client := api.New("http://127.0.0.1:1", "flare_pat_test")
	for _, call := range []string{
		`{"name":"get_operation_series","arguments":{"environment_id":11,"namespace":"job","hours":2}}`,
		`{"name":"get_operation_series","arguments":{"environment_id":11,"namespace":"nope"}}`,
		`{"name":"get_operation_series","arguments":{"namespace":"job"}}`,
		`{"name":"get_namespace_overview","arguments":{"environment_id":11,"namespace":"job","limit":26}}`,
		`{"name":"get_namespace_overview","arguments":{"environment_id":11,"namespace":"job","target":"EventJob"}}`,
		`{"name":"query_metrics","arguments":{"environment_id":11,"group_by":["color"]}}`,
		`{"name":"query_metrics","arguments":{"environment_id":11,"series":true,"limit":26}}`,
		`{"name":"query_metrics","arguments":{"environment_id":11,"sort":"impact"}}`,
		`{"name":"query_metrics","arguments":{"environment_id":11,"group_by":["target","target"]}}`,
	} {
		arguments := call
		input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + call + `}` + "\n")
		var output bytes.Buffer
		if err := serveMCP(context.Background(), client, input, &output); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response["result"].(map[string]any)["isError"] != true {
			t.Fatalf("expected tool error for %s, got %#v", arguments, response)
		}
	}
}

func TestMCPRejectsFractionalAndUnknownArguments(t *testing.T) {
	client := api.New("http://127.0.0.1:1", "flare_pat_test")
	for _, arguments := range []string{
		`{"environment_id":12.9,"namespace":"web"}`,
		`{"environment_id":12,"namespace":"web","unexpected":true}`,
	} {
		input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_namespace_operations","arguments":` + arguments + `}}` + "\n")
		var output bytes.Buffer
		if err := serveMCP(context.Background(), client, input, &output); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.NewDecoder(&output).Decode(&response); err != nil {
			t.Fatal(err)
		}
		result := response["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("expected tool error for %s, got %#v", arguments, response)
		}
	}
}
