package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

type ExchangeResponse struct {
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
	Scopes    []string `json:"scopes"`
}

func Login(ctx context.Context, baseURL string, open func(string) error) (*ExchangeResponse, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start callback listener: %w", err)
	}
	defer listener.Close()

	state, err := randomValue()
	if err != nil {
		return nil, err
	}
	verifier, err := randomValue()
	if err != nil {
		return nil, err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	port := listener.Addr().(*net.TCPAddr).Port

	authorizeURL, err := url.Parse(baseURL + "/cli/authorize")
	if err != nil {
		return nil, err
	}
	query := authorizeURL.Query()
	query.Set("state", state)
	query.Set("port", fmt.Sprint(port))
	query.Set("code_challenge", challenge)
	query.Set("audience", "read_api")
	authorizeURL.RawQuery = query.Encode()

	type callbackResult struct {
		code string
		err  error
	}
	resultChannel := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/callback" {
			http.NotFound(writer, request)
			return
		}
		if request.URL.Query().Get("state") != state {
			http.Error(writer, "State did not match. Return to the terminal and try again.", http.StatusBadRequest)
			resultChannel <- callbackResult{err: errors.New("authorization state did not match")}
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			http.Error(writer, "Authorization code was missing.", http.StatusBadRequest)
			resultChannel <- callbackResult{err: errors.New("authorization code was missing")}
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, "<!doctype html><title>Flare CLI</title><p>Flare CLI is authorized. You can close this window.</p>")
		resultChannel <- callbackResult{code: code}
	})
	go func() { _ = server.Serve(listener) }()

	if open == nil {
		open = OpenBrowser
	}
	if err := open(authorizeURL.String()); err != nil {
		return nil, fmt.Errorf("open browser: %w (open %s manually)", err, authorizeURL.String())
	}

	var callback callbackResult
	select {
	case callback = <-resultChannel:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	if callback.err != nil {
		return nil, callback.err
	}

	payload, _ := json.Marshal(map[string]string{"code": callback.code, "code_verifier": verifier})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/cli/exchange", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("exchange authorization: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exchange authorization: %s", response.Status)
	}
	result := &ExchangeResponse{}
	if err := json.NewDecoder(response.Body).Decode(result); err != nil {
		return nil, fmt.Errorf("decode authorization: %w", err)
	}
	return result, nil
}

func OpenBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	return command.Start()
}

func randomValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
