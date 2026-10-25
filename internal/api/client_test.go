package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGetAuthenticatesAndDecodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer flare_pat_test" {
			t.Fatalf("unexpected authorization header %q", request.Header.Get("Authorization"))
		}
		if request.URL.Query().Get("limit") != "10" {
			t.Fatalf("unexpected query %q", request.URL.RawQuery)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"organizations": []map[string]any{{"id": 1, "name": "Flare"}}})
	}))
	defer server.Close()

	client := New(server.URL, "flare_pat_test")
	var response OrganizationsResponse
	if err := client.Get(context.Background(), "/api/v1/organizations", url.Values{"limit": {"10"}}, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Organizations) != 1 || response.Organizations[0].Name != "Flare" {
		t.Fatalf("unexpected response %#v", response)
	}
}

func TestGetExplainsRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "12")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"detail":"Rate limit exceeded","request_id":"abc"}`))
	}))
	defer server.Close()

	client := New(server.URL, "flare_pat_test")
	err := client.Get(context.Background(), "/api/v1/overviews", nil, &map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "retry in 12s") || !strings.Contains(err.Error(), "request abc") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestGetAddsAuthHintOnlyToUnauthorizedErrors(t *testing.T) {
	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(`{"detail":"A valid personal access token is required.","request_id":"abc"}`))
	}))
	defer server.Close()

	client := New(server.URL, "flare_pat_expired")
	client.AuthHint = "Run `flare-cli auth login`."
	err := client.Get(context.Background(), "/api/v1/organizations", nil, &map[string]any{})
	if err == nil || err.Error() != "A valid personal access token is required. Run `flare-cli auth login`. (request abc)" {
		t.Fatalf("unexpected unauthorized error %v", err)
	}

	status = http.StatusNotFound
	err = client.Get(context.Background(), "/api/v1/organizations", nil, &map[string]any{})
	if err == nil || strings.Contains(err.Error(), "auth login") {
		t.Fatalf("hint should only follow 401s, got %v", err)
	}
}
