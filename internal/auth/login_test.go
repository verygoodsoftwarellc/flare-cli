package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		deliverCallback(t, authorizeURL)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Login(ctx, server.URL, opened, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "flare_pat_test" {
		t.Fatalf("unexpected token %q", result.Token)
	}
}

func TestLoginKeepsWaitingWhenTheBrowserCannotOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(ExchangeResponse{Token: "flare_pat_test"})
	}))
	defer server.Close()

	var status bytes.Buffer
	var openedAddress string
	failingOpen := func(address string) error {
		openedAddress = address
		authorizeURL, err := url.Parse(address)
		if err != nil {
			return err
		}
		// Stands in for the person opening the printed URL by hand.
		deliverCallback(t, authorizeURL)
		return errors.New("xdg-open not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Login(ctx, server.URL, failingOpen, &status)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "flare_pat_test" {
		t.Fatalf("unexpected token %q", result.Token)
	}
	for _, want := range []string{openedAddress, "xdg-open not found"} {
		if !strings.Contains(status.String(), want) {
			t.Fatalf("status %q does not contain %q", status.String(), want)
		}
	}
}

func TestLoginSaysWhyTheExchangeFailed(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"used code", http.StatusNotFound, `{"title":"Invalid Code","detail":"The code is invalid, expired, or already used.","request_id":"req-1"}`, "exchange authorization: The code is invalid, expired, or already used. (request req-1)"},
		{"not JSON", http.StatusBadGateway, `<html>bad gateway</html>`, "exchange authorization: Bad Gateway"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()

			open := func(address string) error {
				authorizeURL, err := url.Parse(address)
				if err != nil {
					return err
				}
				deliverCallback(t, authorizeURL)
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := Login(ctx, server.URL, open, io.Discard)
			if err == nil || err.Error() != test.want {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoginSaysWhenSignInTimedOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, err := Login(ctx, "http://127.0.0.1:1", func(string) error { return nil }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for sign-in") {
		t.Fatalf("err = %v", err)
	}
}

func deliverCallback(t *testing.T, authorizeURL *url.URL) {
	t.Helper()
	query := authorizeURL.Query()
	response, err := http.Get("http://127.0.0.1:" + query.Get("port") + "/callback?code=one-time-code&state=" + url.QueryEscape(query.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}
