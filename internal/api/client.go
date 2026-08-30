package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Verbose    bool
	ErrWriter  io.Writer
}

type APIError struct {
	Status     int
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	RequestID  string `json:"request_id"`
	RetryAfter string
}

func (err *APIError) Error() string {
	detail := err.Detail
	if detail == "" {
		detail = http.StatusText(err.Status)
	}
	if err.Status == http.StatusTooManyRequests && err.RetryAfter != "" {
		detail += "; retry in " + err.RetryAfter + "s"
	}
	if err.RequestID != "" {
		detail += " (request " + err.RequestID + ")"
	}
	return detail
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		ErrWriter:  io.Discard,
	}
}

func (client *Client) Get(ctx context.Context, path string, query url.Values, output any) error {
	requestURL := client.BaseURL + path
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.Token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "flare-cli/dev")

	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("request Flare: %w", err)
	}
	defer response.Body.Close()
	if client.Verbose {
		fmt.Fprintf(client.ErrWriter, "rate-limit: %s policy=%s request=%s\n", response.Header.Get("RateLimit"), response.Header.Get("RateLimit-Policy"), response.Header.Get("X-Request-ID"))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiError := &APIError{Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(apiError)
		return apiError
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 10<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode Flare response: %w", err)
	}
	return nil
}

func AddInt(query url.Values, key string, value int64) {
	if value != 0 {
		query.Set(key, strconv.FormatInt(value, 10))
	}
}
