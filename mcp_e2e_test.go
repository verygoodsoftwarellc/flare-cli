package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/verygoodsoftwarellc/flare-cli/internal/command"
)

func TestMCPCommandEndToEnd(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/organizations" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"organizations":[{"id":1,"name":"Flare"}],"pagination":{"next":null,"previous":null}}`))
	}))
	defer apiServer.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FLARE_API_URL", apiServer.URL)
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	process := exec.Command(os.Args[0], "-test.run=^TestMCPHelperProcess$")
	process.Env = append(os.Environ(), "GO_WANT_MCP_HELPER=1")
	var stderr bytes.Buffer
	process.Stderr = &stderr
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "flare-cli-e2e", Version: "dev"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: process}, nil)
	if err != nil {
		t.Fatalf("connect to flare-cli MCP server: %v\nstderr: %s", err, stderr.String())
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v\nstderr: %s", err, stderr.String())
	}
	if len(tools.Tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools.Tools))
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_organizations", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("call list_organizations: result=%#v err=%v\nstderr: %s", result, err, stderr.String())
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	os.Args = []string{"flare-cli", "mcp"}
	if err := command.Execute("flare-cli"); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
