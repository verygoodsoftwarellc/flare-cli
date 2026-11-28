package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

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
// hosted MCP server, passing each message through as it is.
type mcpProxy struct {
	hosted *hostedMCP

	writeMutex sync.Mutex
	output     io.Writer
}

func newMCPProxy(client *api.Client) *mcpProxy {
	return &mcpProxy{hosted: newHostedMCP(client)}
}

func (proxy *mcpProxy) run(ctx context.Context, input io.Reader, output io.Writer) error {
	proxy.output = output
	reader := bufio.NewReaderSize(input, 64<<10)
	var pending sync.WaitGroup
	defer func() {
		pending.Wait()
		proxy.hosted.endSession()
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

// forward sends one message and writes Flare's replies, answering a request
// with an error when none of them did.
func (proxy *mcpProxy) forward(ctx context.Context, message []byte, envelope rpcEnvelope) {
	ctx, cancel := context.WithTimeout(ctx, proxy.hosted.timeout)
	defer cancel()
	replies := &replyWriter{proxy: proxy, requestID: compactID(envelope.ID), initialize: envelope.Method == "initialize"}
	err := proxy.hosted.send(ctx, message, replies.write)
	if !envelope.isRequest() || replies.answered {
		return
	}
	proxy.writeError(envelope.ID, proxy.hosted.unanswered(ctx, err))
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
	if json.Unmarshal(message, &envelope) == nil && envelope.answers(replies.requestID) {
		replies.answered = true
		if replies.initialize && envelope.Result != nil {
			// Set before the client sees the answer, so its next
			// message carries the version.
			replies.proxy.hosted.noteInitialized(envelope.Result)
		}
	}
	replies.proxy.writeLine(message)
}
