package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

// maxMCPMessage caps one message read from stdin or from Flare.
const maxMCPMessage = 10 << 20

func newMCPCommand(options *rootOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Connect a stdio MCP client to Flare's hosted MCP server",
		Long: "Read MCP messages on stdin, forward each one to Flare's hosted MCP server (<api url>/mcp) with your " +
			"Flare token, and write its replies on stdout, for MCP clients that only run local servers.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := options.client()
			if err != nil {
				return err
			}
			return newMCPProxy(client).run(command.Context(), options.input, options.output)
		},
	}
	command.AddCommand(newMCPInstallCommand(options), newMCPConfigCommand(options))
	return command
}

// mcpProxy relays newline-delimited JSON-RPC between stdio and Flare's
// Streamable HTTP MCP endpoint. It doesn't open the optional GET stream for
// server-initiated messages; Flare's tools answer each request in its reply.
type mcpProxy struct {
	endpoint   string
	httpClient *http.Client
	authHint   string
	scopeHint  string

	mutex           sync.Mutex
	sessionID       string
	protocolVersion string

	writeMutex sync.Mutex
	output     io.Writer
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result *struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"result"`
}

// isRequest is true for a message that expects an answer, as opposed to a
// notification or a response to a request from the server.
func (envelope rpcEnvelope) isRequest() bool {
	return envelope.Method != "" && len(envelope.ID) > 0 && string(envelope.ID) != "null"
}

func newMCPProxy(client *api.Client) *mcpProxy {
	return &mcpProxy{
		endpoint:   client.BaseURL + "/mcp",
		httpClient: remoteHTTPClient(client.Token),
		authHint:   client.AuthHint,
		scopeHint:  client.ScopeHint,
	}
}

func (proxy *mcpProxy) run(ctx context.Context, input io.Reader, output io.Writer) error {
	proxy.output = output
	reader := bufio.NewReaderSize(input, 64<<10)
	var pending sync.WaitGroup
	defer func() {
		pending.Wait()
		proxy.endSession()
	}()
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > maxMCPMessage {
			return fmt.Errorf("MCP message over %d bytes", maxMCPMessage)
		}
		if message := bytes.TrimSpace(line); len(message) > 0 {
			var envelope rpcEnvelope
			// A message that doesn't parse goes to Flare anyway, which
			// answers it with a JSON-RPC parse error.
			_ = json.Unmarshal(message, &envelope)
			if envelope.isRequest() && envelope.Method != "initialize" {
				// Requests run side by side, so a slow tool call doesn't
				// hold up the others or a notification cancelling it.
				pending.Add(1)
				go func() {
					defer pending.Done()
					proxy.forward(ctx, message, envelope)
				}()
			} else {
				// Everything else goes in order: later messages need the
				// session initialize starts, and Flare should see
				// notifications/initialized before the requests after it,
				// even from a client that doesn't wait for the answers.
				proxy.forward(ctx, message, envelope)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (proxy *mcpProxy) forward(ctx context.Context, message []byte, envelope rpcEnvelope) {
	answered, err := proxy.post(ctx, message, envelope)
	if !envelope.isRequest() || answered {
		return
	}
	if err == nil {
		err = errors.New("Flare ended the response without answering")
	}
	proxy.writeError(envelope.ID, err)
}

// post sends one message and writes Flare's replies, reporting whether one of
// them answered the message.
func (proxy *mcpProxy) post(ctx context.Context, message []byte, envelope rpcEnvelope) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.endpoint, bytes.NewReader(message))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	proxy.mutex.Lock()
	if proxy.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", proxy.sessionID)
	}
	if proxy.protocolVersion != "" {
		request.Header.Set("Mcp-Protocol-Version", proxy.protocolVersion)
	}
	proxy.mutex.Unlock()

	response, err := proxy.httpClient.Do(request)
	if err != nil {
		return false, fmt.Errorf("request Flare: %w", err)
	}
	defer response.Body.Close()
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		proxy.mutex.Lock()
		proxy.sessionID = sessionID
		proxy.mutex.Unlock()
	}

	status := response.StatusCode
	switch {
	case status == http.StatusAccepted:
		return false, nil
	case status >= 300 && status < 400:
		return false, fmt.Errorf("Flare redirected %s to %q; not following it with your token", proxy.endpoint, response.Header.Get("Location"))
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		apiError := api.ReadError(response)
		apiError.Hint = proxy.authHint
		if status == http.StatusForbidden {
			apiError.Hint = proxy.scopeHint
		}
		return false, apiError
	}

	replies := &replyWriter{proxy: proxy, requestID: compactID(envelope.ID), initialize: envelope.Method == "initialize"}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" && status < 300 {
		err := readEvents(response.Body, replies.write)
		return replies.answered, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMCPMessage))
	if err != nil {
		return false, fmt.Errorf("read Flare response: %w", err)
	}
	if status >= 300 {
		// A JSON-RPC error in the body says more than the status does.
		if json.Valid(body) && bytes.Contains(body, []byte(`"jsonrpc"`)) {
			replies.write(body)
			return replies.answered, nil
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		return false, api.ReadError(response)
	}
	if len(bytes.TrimSpace(body)) > 0 {
		replies.write(body)
	}
	return replies.answered, nil
}

// endSession tells Flare the client is done, as the transport asks clients to.
func (proxy *mcpProxy) endSession() {
	proxy.mutex.Lock()
	sessionID := proxy.sessionID
	proxy.mutex.Unlock()
	if sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, proxy.endpoint, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", sessionID)
	if response, err := proxy.httpClient.Do(request); err == nil {
		response.Body.Close()
	}
}

func (proxy *mcpProxy) writeError(id json.RawMessage, err error) {
	message, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32000, "message": err.Error()},
	})
	proxy.writeLine(message)
}

// writeLine writes one message on its own line, as the stdio transport
// requires, so a pretty-printed reply is compacted first.
func (proxy *mcpProxy) writeLine(message []byte) {
	var line bytes.Buffer
	if err := json.Compact(&line, message); err != nil {
		line.Reset()
		line.Write(bytes.ReplaceAll(message, []byte("\n"), nil))
	}
	line.WriteByte('\n')
	proxy.writeMutex.Lock()
	defer proxy.writeMutex.Unlock()
	_, _ = proxy.output.Write(line.Bytes())
}

// replyWriter writes the replies to one request and notes whether one of
// them answered it.
type replyWriter struct {
	proxy      *mcpProxy
	requestID  string
	initialize bool
	answered   bool
}

func (replies *replyWriter) write(message []byte) {
	var envelope rpcEnvelope
	if json.Unmarshal(message, &envelope) == nil && envelope.Method == "" && compactID(envelope.ID) == replies.requestID {
		replies.answered = true
		if replies.initialize && envelope.Result != nil {
			// Set before the client sees the answer, so its next
			// message carries the version.
			replies.proxy.mutex.Lock()
			replies.proxy.protocolVersion = envelope.Result.ProtocolVersion
			replies.proxy.mutex.Unlock()
		}
	}
	replies.proxy.writeLine(message)
}

func compactID(id json.RawMessage) string {
	var compacted bytes.Buffer
	if json.Compact(&compacted, id) != nil {
		return string(id)
	}
	return compacted.String()
}

// readEvents calls write with the data of each message event in a
// text/event-stream body.
func readEvents(body io.Reader, write func([]byte)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxMCPMessage)
	event := ""
	var data [][]byte
	dispatch := func() {
		if len(data) > 0 && (event == "" || event == "message") {
			write(bytes.Join(data, []byte("\n")))
		}
		event, data = "", nil
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			dispatch()
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			event = string(value)
		case "data":
			data = append(data, bytes.Clone(value))
		}
	}
	dispatch()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Flare event stream: %w", err)
	}
	return nil
}
