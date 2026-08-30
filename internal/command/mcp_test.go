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
	if len(tools) != 5 {
		t.Fatalf("expected 5 curated tools, got %d", len(tools))
	}
	structured := callResponse["result"].(map[string]any)["structuredContent"].(map[string]any)
	if _, ok := structured["organizations"]; !ok {
		t.Fatalf("unexpected result %#v", structured)
	}
}
