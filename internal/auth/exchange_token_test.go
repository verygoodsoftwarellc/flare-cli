package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginRejectsIncompleteExchangeResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"token":null}`, `{"token":""}`, `{"token":"  "}`, `{"token":"\n\t"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			open := func(address string) error {
				u, err := url.Parse(address)
				if err != nil {
					return err
				}
				deliverCallback(t, u)
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := Login(ctx, server.URL, open, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "did not include a token") {
				t.Fatalf("expected missing-token error,got resultPresent=%t err=%v", result != nil, err)
			}
			if result != nil {
				t.Fatal("failed exchange returned a result")
			}
		})
	}
}
