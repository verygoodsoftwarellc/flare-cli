package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestLoginUsesReadPurposeLoopbackAndPKCE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/cli/exchange" {
			http.NotFound(writer, request)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "one-time-code" || body["code_verifier"] == "" {
			t.Fatalf("unexpected exchange body %#v", body)
		}
		_ = json.NewEncoder(writer).Encode(ExchangeResponse{Token: "flare_pat_test", Scopes: []string{"org:read"}})
	}))
	defer server.Close()

	opened := func(address string) error {
		authorizeURL, err := url.Parse(address)
		if err != nil {
			return err
		}
		if authorizeURL.Query().Get("purpose") != "read_api" || authorizeURL.Query().Get("code_challenge") == "" {
			t.Fatalf("unexpected authorize URL %s", address)
		}
		callback := "http://127.0.0.1:" + authorizeURL.Query().Get("port") + "/callback?code=one-time-code&state=" + url.QueryEscape(authorizeURL.Query().Get("state"))
		go func() {
			response, err := http.Get(callback)
			if err != nil {
				t.Error(err)
				return
			}
			_ = response.Body.Close()
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Login(ctx, server.URL, opened)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "flare_pat_test" {
		t.Fatalf("unexpected token %q", result.Token)
	}
}
