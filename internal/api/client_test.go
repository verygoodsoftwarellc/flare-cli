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
	testCases := []struct {
		name       string
		retryAfter string
		wantHint   string
	}{
		{"seconds", "12", "; retry in 12s"},
		{"large delay", "18446744073709551616", "; retry in 18446744073709551616s"},
		{"date", "Fri, 31 Dec 2027 23:59:59 GMT", "; retry after Fri, 31 Dec 2027 23:59:59 GMT"},
		{"legacy RFC 850 date", "Friday, 31-Dec-27 23:59:59 GMT", "; retry after Fri, 31 Dec 2027 23:59:59 GMT"},
		{"legacy asctime date", "Fri Dec 31 23:59:59 2027", "; retry after Fri, 31 Dec 2027 23:59:59 GMT"},
		{"invalid", "soon", ""},
		{"absent", "", ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if testCase.retryAfter != "" {
					writer.Header().Set("Retry-After", testCase.retryAfter)
				}
				writer.WriteHeader(http.StatusTooManyRequests)
				_, _ = writer.Write([]byte(`{"detail":"Rate limit exceeded","request_id":"abc"}`))
			}))
			defer server.Close()

			client := New(server.URL, "flare_pat_test")
			err := client.Get(context.Background(), "/api/v1/overviews", nil, &map[string]any{})
			want := "Rate limit exceeded" + testCase.wantHint + " (request abc)"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
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

func TestGetAddsScopeHintToForbiddenErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"detail":"This token requires the traces:read scope.","request_id":"abc"}`))
	}))
	defer server.Close()

	client := New(server.URL, "flare_pat_old")
	client.AuthHint = "Run `flare-cli auth login` to sign in again."
	client.ScopeHint = "Run `flare-cli auth login` to get a token with every scope."
	err := client.Get(context.Background(), "/api/v1/traces", nil, &map[string]any{})
	if err == nil || err.Error() != "This token requires the traces:read scope. Run `flare-cli auth login` to get a token with every scope. (request abc)" {
		t.Fatalf("unexpected forbidden error %v", err)
	}
}
