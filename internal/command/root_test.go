package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/config"
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

func TestStatsCellsShowDurationsLikeTheWebPages(t *testing.T) {
	tests := []struct {
		sum     int64
		avg     float64
		sumCell string
		avgCell string
	}{
		{0, 0, "0ms", "0ms"},
		{999, 1.99, "999ms", "2ms"},
		{1_500, 1_500, "1.5s", "1.5s"},
		{90_000, 90_000, "1.5m", "90.0s"},
		{18_000_000, 98.72, "5.0h", "99ms"},
	}
	for _, test := range tests {
		cells := statsCells(1, test.sum, test.avg, 0)
		if cells[1] != test.sumCell || cells[2] != test.avgCell {
			t.Errorf("statsCells(sum %d, avg %v) = %q, %q; want %q, %q", test.sum, test.avg, cells[1], cells[2], test.sumCell, test.avgCell)
		}
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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

func TestZeroImpactSurvivesJSONRoundTrip(t *testing.T) {
	var group api.QueryGroup
	if err := json.Unmarshal([]byte(`{"selector":{},"label":"x","count":0,"sum":0,"avg":0,"error_count":0,"error_rate":0,"impact":0,"series":null}`), &group); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"impact":0`) {
		t.Fatalf("zero impact was dropped: %s", encoded)
	}
	var stats api.Stats
	if err := json.Unmarshal([]byte(`{"count":1,"sum":2,"avg":2,"error_count":0,"error_rate":0}`), &stats); err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(stats); strings.Contains(string(encoded), "impact") {
		t.Fatalf("absent impact was invented: %s", encoded)
	}
}

func TestNamespaceCommandPagesWithCursor(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cursors = append(cursors, request.URL.Query().Get("cursor"))
		next := `"page-2"`
		if request.URL.Query().Get("cursor") != "" {
			next = "null"
		}
		_, _ = writer.Write([]byte(`{"environment":{"id":1,"project_id":1,"name":"Production"},"namespace":"web","operations":[],"pagination":{"next":` + next + `,"previous":null}}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	run := func(args ...string) string {
		errOutput := &bytes.Buffer{}
		options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: &bytes.Buffer{}, errors: errOutput}
		command := newNamespaceCommand(options)
		command.SetArgs(append([]string{"-e", "1", "-n", "web"}, args...))
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		return errOutput.String()
	}
	if hint := run(); !strings.Contains(hint, "--cursor page-2") {
		t.Fatalf("first page should say how to get the next one, got %q", hint)
	}
	if hint := run("--cursor", "page-2"); hint != "" {
		t.Fatalf("last page should not hint, got %q", hint)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "page-2" {
		t.Fatalf("cursors = %q", cursors)
	}
}

func TestQueryRejectsNegativeMinCount(t *testing.T) {
	root := newRootCommand("flare")
	root.SetArgs([]string{"metrics", "query", "--environment", "1", "--min-count", "-1"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "min-count") {
		t.Fatalf("expected min-count rejection, got %v", err)
	}
}

func TestUnauthorizedResponsesExplainHowToSignInAgain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"title":"Unauthorized","detail":"A valid personal access token is required."}`))
	}))
	defer server.Close()
	stored := &config.Config{APIURL: server.URL, Token: "flare_pat_expired", TokenBackend: "file", TokenAPIURL: server.URL}
	if err := stored.Save(); err != nil {
		t.Fatal(err)
	}

	const detail = "A valid personal access token is required. "
	const sameURL = " with the same --api-url"
	// A one-off --api-url is mentioned in words. FLARE_API_URL persists, so
	// the plain command already signs in to the same server.
	cases := []struct {
		flag, envToken bool
		want           string
	}{
		{flag: true, want: detail + "Run `flare-cli auth login`" + sameURL + " to sign in again."},
		{flag: true, envToken: true, want: detail + "FLARE_TOKEN was rejected; replace it, or unset it and run `flare-cli auth login`" + sameURL + "."},
		{want: detail + "Run `flare-cli auth login` to sign in again."},
		{envToken: true, want: detail + "FLARE_TOKEN was rejected; replace it, or unset it and run `flare-cli auth login`."},
	}
	for _, test := range cases {
		t.Setenv("FLARE_TOKEN", "")
		if test.envToken {
			t.Setenv("FLARE_TOKEN", "flare_pat_revoked")
		}
		t.Setenv("FLARE_API_URL", server.URL)
		for _, args := range [][]string{{"org", "list"}, {"auth", "status"}} {
			if test.flag {
				args = append(args, "--api-url", server.URL)
			}
			root := newRootCommand("flare-cli")
			root.SetArgs(args)
			if err := root.ExecuteContext(context.Background()); err == nil || err.Error() != test.want {
				t.Fatalf("%v (FLARE_TOKEN set: %v): expected %q, got %v", args, test.envToken, test.want, err)
			}
		}
	}
}
