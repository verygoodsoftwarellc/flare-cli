package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

func TestCatalogHelpersFollowOpaqueNextCursors(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			if request.URL.Query().Get("cursor") != "" {
				t.Fatalf("unexpected first cursor %q", request.URL.Query().Get("cursor"))
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"organizations": []map[string]any{{"id": 1, "name": "One"}},
				"pagination":    map[string]any{"next": "opaque-next", "previous": nil},
			})
			return
		}
		if request.URL.Query().Get("cursor") != "opaque-next" {
			t.Fatalf("missing opaque cursor on second request")
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"organizations": []map[string]any{{"id": 2, "name": "Two"}},
			"pagination":    map[string]any{"next": nil, "previous": "opaque-previous"},
		})
	}))
	defer server.Close()

	response, err := allOrganizations(context.Background(), api.New(server.URL, "flare_pat_test"))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(response.Organizations) != 2 {
		t.Fatalf("expected two pages, got %d requests and %#v", requests, response)
	}
}

func TestLoginDoesNotAcceptTokenInProcessArguments(t *testing.T) {
	root := newRootCommand("flare")
	authCommand, _, err := root.Find([]string{"auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	if authCommand.Flags().Lookup("token") != nil {
		t.Fatal("login still exposes a value-bearing --token flag")
	}
	if authCommand.Flags().Lookup("with-token") == nil {
		t.Fatal("login should accept tokens through stdin")
	}
}

func TestAuthenticationErrorsUseInvokedExecutableName(t *testing.T) {
	root := newRootCommand("flare-cli")
	if root.Use != "flare-cli" {
		t.Fatalf("expected invoked executable in usage, got %q", root.Use)
	}

	options := &rootOptions{name: "flare-cli"}
	err := options.authenticationError(errors.New("not authenticated"))
	if got := err.Error(); got != "not authenticated; run `flare-cli auth login` or set FLARE_TOKEN" {
		t.Fatalf("unexpected authentication error %q", got)
	}
}

func TestProjectRowsShowOrganizationNames(t *testing.T) {
	projects := []api.Project{
		{ID: 10, OrganizationID: 1, Name: "Storefront"},
		{ID: 20, OrganizationID: 2, Name: "Worker"},
	}
	organizations := []api.Organization{{ID: 1, Name: "Acme"}}

	rows := projectRows(projects, organizations)
	want := [][]string{{"10", "Storefront", "Acme"}, {"20", "Worker", "2"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("unexpected project rows %#v", rows)
	}
}

func TestEnvironmentRowsShowProjectAndOrganizationNames(t *testing.T) {
	environments := []api.Environment{
		{ID: 100, ProjectID: 10, Name: "Production"},
		{ID: 200, ProjectID: 20, Name: "Staging"},
		{ID: 300, ProjectID: 30, Name: "Development"},
	}
	projects := []api.Project{
		{ID: 10, OrganizationID: 1, Name: "Storefront"},
		{ID: 20, OrganizationID: 2, Name: "Worker"},
	}
	organizations := []api.Organization{{ID: 1, Name: "Acme"}}

	rows := environmentRows(environments, projects, organizations)
	want := [][]string{
		{"100", "Production", "Storefront", "Acme"},
		{"200", "Staging", "Worker", "2"},
		{"300", "Development", "30", "-"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("unexpected environment rows %#v", rows)
	}
}

func TestProjectListResolvesOrganizationsWithBulkRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/api/v1/projects":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"projects":   []map[string]any{{"id": 10, "organization_id": 1, "name": "Storefront"}},
				"pagination": map[string]any{"next": nil, "previous": nil},
			})
		case "/api/v1/organizations":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"organizations": []map[string]any{{"id": 1, "name": "Acme"}},
				"pagination":    map[string]any{"next": nil, "previous": nil},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}, input: strings.NewReader("")}
	command := newProjectCommand(options)
	command.SetArgs([]string{"list"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests["/api/v1/projects"] != 1 || requests["/api/v1/organizations"] != 1 {
		t.Fatalf("expected one bulk request per resource, got %#v", requests)
	}
	if got := output.String(); !strings.Contains(got, "ID  NAME        ORGANIZATION") || !strings.Contains(got, "10  Storefront  Acme") {
		t.Fatalf("unexpected project table:\n%s", got)
	}
}

func TestEnvironmentListResolvesParentsWithBulkRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/api/v1/environments":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"environments": []map[string]any{{"id": 100, "project_id": 10, "name": "Production"}},
				"pagination":   map[string]any{"next": nil, "previous": nil},
			})
		case "/api/v1/projects":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"projects":   []map[string]any{{"id": 10, "organization_id": 1, "name": "Storefront"}},
				"pagination": map[string]any{"next": nil, "previous": nil},
			})
		case "/api/v1/organizations":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"organizations": []map[string]any{{"id": 1, "name": "Acme"}},
				"pagination":    map[string]any{"next": nil, "previous": nil},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}, input: strings.NewReader("")}
	command := newEnvironmentCommand(options)
	command.SetArgs([]string{"list"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests["/api/v1/environments"] != 1 || requests["/api/v1/projects"] != 1 || requests["/api/v1/organizations"] != 1 {
		t.Fatalf("expected one bulk request per resource, got %#v", requests)
	}
	if got := output.String(); !strings.Contains(got, "ID   NAME        PROJECT     ORGANIZATION") || !strings.Contains(got, "100  Production  Storefront  Acme") {
		t.Fatalf("unexpected environment table:\n%s", got)
	}
}

func TestVerifyAuthenticationChecksTokenWithServer(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		shouldFail bool
	}{
		{name: "valid token", status: http.StatusOK},
		{name: "valid token without org scope", status: http.StatusForbidden},
		{name: "invalid token", status: http.StatusUnauthorized, shouldFail: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/v1/organizations" || request.URL.Query().Get("limit") != "1" {
					t.Fatalf("unexpected status request %s", request.URL.String())
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer server.Close()

			err := verifyAuthentication(context.Background(), api.New(server.URL, "flare_pat_test"))
			if test.shouldFail && err == nil {
				t.Fatal("expected authentication check to fail")
			}
			if !test.shouldFail && err != nil {
				t.Fatalf("expected authentication check to pass, got %v", err)
			}
		})
	}
}
