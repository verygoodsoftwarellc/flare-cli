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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/version"
)

const (
	// maxMCPMessage caps one message read from stdin or from Flare.
	maxMCPMessage = 10 << 20
	// mcpRequestTimeout bounds one exchange with Flare, event stream
	// included. Flare's tools answer in seconds; this only stops a stalled
	// request from hanging the client, or the exit after stdin closes.
	mcpRequestTimeout = 2 * time.Minute
	// mcpProtocolVersion is what `tools`, `call` and `setup` ask for in
	// initialize. The bridge passes on whatever its client asks for.
	mcpProtocolVersion = "2025-11-25"
)

// hostedMCP talks to Flare's hosted MCP server (<api url>/mcp) over
// Streamable HTTP with the user's token. The stdio bridge relays messages
// through send as they are; `tools`, `call` and `setup` use its requests.
// It doesn't open the optional GET stream for server-initiated messages;
// Flare's tools answer each request in its reply.
type hostedMCP struct {
	endpoint   string
	httpClient *http.Client
	authHint   string
	scopeHint  string
	timeout    time.Duration
	lastID     atomic.Int64

	mutex           sync.Mutex
	sessionID       string
	protocolVersion string
}

// rpcEnvelope is the part of a JSON-RPC message the client looks at.
type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Error is the message plus its data, where Flare puts the detail, as in
// "Invalid params: Tool not found: nope".
func (err *rpcError) Error() string {
	message := err.Message
	if len(err.Data) > 0 && string(err.Data) != "null" {
		message += ": " + schemaValue(err.Data)
	}
	return withoutControls(message)
}

// isRequest is true for a message that expects an answer, as opposed to a
// notification or a response to a request from the server.
func (envelope rpcEnvelope) isRequest() bool {
	return envelope.Method != "" && len(envelope.ID) > 0 && string(envelope.ID) != "null"
}

// answers is true for the response to the request with this compacted id.
func (envelope rpcEnvelope) answers(requestID string) bool {
	return envelope.Method == "" && compactID(envelope.ID) == requestID
}

// toolResult is a tools/call result.
type toolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent"`
	IsError           bool              `json:"isError"`
}

func newHostedMCP(client *api.Client) *hostedMCP {
	hosted := newHostedMCPAt(client.BaseURL+"/mcp", client.Token)
	hosted.authHint = client.AuthHint
	hosted.scopeHint = client.ScopeHint
	return hosted
}

func newHostedMCPAt(endpoint, token string) *hostedMCP {
	return &hostedMCP{endpoint: endpoint, httpClient: remoteHTTPClient(token), timeout: mcpRequestTimeout}
}

// connectHostedMCP signs in to Flare's hosted MCP server with the CLI's
// token. Close the session with endSession.
func (options *rootOptions) connectHostedMCP(ctx context.Context) (*hostedMCP, error) {
	client, err := options.client()
	if err != nil {
		return nil, err
	}
	hosted := newHostedMCP(client)
	if err := hosted.initialize(ctx); err != nil {
		hosted.endSession()
		return nil, err
	}
	return hosted, nil
}

// initialize opens a session, as every MCP client must before other requests.
func (hosted *hostedMCP) initialize(ctx context.Context) error {
	result, err := hosted.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "flare-cli", "version": version.Current()},
	})
	if err != nil {
		return err
	}
	hosted.noteInitialized(result)
	return hosted.notify(ctx, "notifications/initialized")
}

// noteInitialized keeps the protocol version from an initialize result, which
// every later request carries in a header.
func (hosted *hostedMCP) noteInitialized(result json.RawMessage) {
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &initialized) != nil {
		return
	}
	hosted.mutex.Lock()
	hosted.protocolVersion = initialized.ProtocolVersion
	hosted.mutex.Unlock()
}

// listTools returns every tool the server lists, following its cursors.
func (hosted *hostedMCP) listTools(ctx context.Context) ([]json.RawMessage, error) {
	tools := []json.RawMessage{}
	seen := map[string]bool{}
	var params any
	for {
		result, err := hosted.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, fmt.Errorf("decode tools/list result: %w", err)
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools, nil
		}
		if seen[page.NextCursor] {
			return nil, errors.New("tools/list repeated a pagination cursor")
		}
		seen[page.NextCursor] = true
		params = map[string]string{"cursor": page.NextCursor}
	}
}

// callTool runs one tool. A tool that fails reports it in the result's
// IsError, not as an error here.
func (hosted *hostedMCP) callTool(ctx context.Context, name string, arguments map[string]json.RawMessage) (toolResult, error) {
	if arguments == nil {
		arguments = map[string]json.RawMessage{}
	}
	result, err := hosted.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return toolResult{}, err
	}
	var called toolResult
	if err := json.Unmarshal(result, &called); err != nil {
		return toolResult{}, fmt.Errorf("decode tools/call result: %w", err)
	}
	return called, nil
}

// request sends one JSON-RPC request and returns its result, or its error.
func (hosted *hostedMCP) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := hosted.lastID.Add(1)
	message, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, hosted.timeout)
	defer cancel()
	requestID := strconv.FormatInt(id, 10)
	var answer *rpcEnvelope
	err = hosted.send(ctx, message, func(reply []byte) {
		var envelope rpcEnvelope
		if json.Unmarshal(reply, &envelope) == nil && envelope.answers(requestID) {
			answer = &envelope
		}
	})
	if answer == nil {
		return nil, hosted.unanswered(ctx, err)
	}
	if answer.Error != nil {
		return nil, answer.Error
	}
	return answer.Result, nil
}

func (hosted *hostedMCP) notify(ctx context.Context, method string) error {
	message, err := json.Marshal(map[string]string{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, hosted.timeout)
	defer cancel()
	return hosted.send(ctx, message, func([]byte) {})
}

// unanswered says why a request sent under ctx got no answer.
func (hosted *hostedMCP) unanswered(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("Flare didn't answer within %s", hosted.timeout)
	case err == nil:
		return errors.New("Flare ended the response without answering")
	}
	return err
}

// send posts one message and passes each message Flare sends back, in order,
// to receive.
func (hosted *hostedMCP) send(ctx context.Context, message []byte, receive func([]byte)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, hosted.endpoint, bytes.NewReader(message))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	hosted.mutex.Lock()
	if hosted.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", hosted.sessionID)
	}
	if hosted.protocolVersion != "" {
		request.Header.Set("Mcp-Protocol-Version", hosted.protocolVersion)
	}
	hosted.mutex.Unlock()

	response, err := hosted.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request Flare: %w", err)
	}
	defer response.Body.Close()
	if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
		hosted.mutex.Lock()
		hosted.sessionID = sessionID
		hosted.mutex.Unlock()
	}

	status := response.StatusCode
	switch {
	case status == http.StatusAccepted:
		return nil
	case status >= 300 && status < 400:
		return fmt.Errorf("Flare redirected %s to %q; not following it with your token", hosted.endpoint, response.Header.Get("Location"))
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		apiError := api.ReadError(response)
		apiError.Hint = hosted.authHint
		if status == http.StatusForbidden {
			apiError.Hint = hosted.scopeHint
		}
		return apiError
	}

	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" && status < 300 {
		return readEvents(response.Body, receive)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMCPMessage))
	if err != nil {
		return fmt.Errorf("read Flare response: %w", err)
	}
	if status >= 300 {
		// A JSON-RPC error in the body says more than the status does.
		if json.Valid(body) && bytes.Contains(body, []byte(`"jsonrpc"`)) {
			receive(body)
			return nil
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		return api.ReadError(response)
	}
	if len(bytes.TrimSpace(body)) > 0 {
		receive(body)
	}
	return nil
}

// endSession tells Flare the client is done, as the transport asks clients to.
func (hosted *hostedMCP) endSession() {
	hosted.mutex.Lock()
	sessionID := hosted.sessionID
	hosted.mutex.Unlock()
	if sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, hosted.endpoint, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", sessionID)
	if response, err := hosted.httpClient.Do(request); err == nil {
		response.Body.Close()
	}
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
