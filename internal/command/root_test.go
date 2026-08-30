package command

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	root := newRootCommand()
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
