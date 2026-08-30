package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func newMCPCommand(options *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run a read-only MCP server over stdio",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := options.client()
			if err != nil {
				return err
			}
			return serveMCP(command.Context(), client, command.InOrStdin(), command.OutOrStdout())
		},
	}
}

func serveMCP(ctx context.Context, client *api.Client, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "Parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		response := handleMCPRequest(ctx, client, request)
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleMCPRequest(ctx context.Context, client *api.Client, request mcpRequest) mcpResponse {
	response := mcpResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "flare", "version": "dev"},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		var call toolCall
		if err := json.Unmarshal(request.Params, &call); err != nil {
			response.Error = &mcpError{Code: -32602, Message: "Invalid tool arguments"}
			return response
		}
		result, err := callMCPTool(ctx, client, call)
		if err != nil {
			response.Result = map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
			}
			return response
		}
		encoded, _ := json.Marshal(result)
		response.Result = map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(encoded)}},
			"structuredContent": result,
		}
	default:
		response.Error = &mcpError{Code: -32601, Message: "Method not found"}
	}
	return response
}

func mcpTools() []map[string]any {
	readOnly := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	integer := map[string]any{"type": "integer"}
	return []map[string]any{
		{
			"name": "list_organizations", "description": "List Flare organizations the user can access.",
			"inputSchema": objectSchema(map[string]any{}, nil), "annotations": readOnly,
		},
		{
			"name": "list_projects", "description": "List accessible Flare projects, optionally filtered by organization ID.",
			"inputSchema": objectSchema(map[string]any{"organization_id": integer}, nil), "annotations": readOnly,
		},
		{
			"name": "list_environments", "description": "List accessible Flare environments, optionally filtered by project ID.",
			"inputSchema": objectSchema(map[string]any{"project_id": integer}, nil), "annotations": readOnly,
		},
		{
			"name": "get_environment_overviews", "description": "Get bulk performance summaries for up to 25 Flare environments.",
			"inputSchema": objectSchema(map[string]any{
				"environment_ids": map[string]any{"type": "array", "items": integer, "minItems": 1, "maxItems": 25},
				"hours":           map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
				"sort":            map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate", "impact"}, "default": "sum"},
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 25, "default": 5},
			}, []string{"environment_ids"}), "annotations": readOnly,
		},
		{
			"name": "list_namespace_operations", "description": "List top operations for one Flare environment namespace.",
			"inputSchema": objectSchema(map[string]any{
				"environment_id": integer,
				"namespace":      map[string]any{"type": "string", "enum": []string{"web", "job", "db", "http", "cache", "view"}},
				"hours":          map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
				"sort":           map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate", "impact"}, "default": "sum"},
				"direction":      map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "default": "desc"},
				"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25},
				"q":              map[string]any{"type": "string", "maxLength": 255},
			}, []string{"environment_id", "namespace"}), "annotations": readOnly,
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func callMCPTool(ctx context.Context, client *api.Client, call toolCall) (map[string]any, error) {
	query := url.Values{"limit": {"100"}}
	path := ""
	switch call.Name {
	case "list_organizations":
		path = "/api/v1/organizations"
	case "list_projects":
		path = "/api/v1/projects"
		addArgument(query, "organization_id", call.Arguments["organization_id"])
	case "list_environments":
		path = "/api/v1/environments"
		addArgument(query, "project_id", call.Arguments["project_id"])
	case "get_environment_overviews":
		path = "/api/v1/overviews"
		ids, ok := call.Arguments["environment_ids"].([]any)
		if !ok || len(ids) == 0 {
			return nil, fmt.Errorf("environment_ids is required")
		}
		values := make([]string, len(ids))
		for index, id := range ids {
			values[index] = numberString(id)
		}
		query.Set("environment_ids", strings.Join(values, ","))
		addDefaultArgument(query, "hours", call.Arguments["hours"], "24")
		addDefaultArgument(query, "sort", call.Arguments["sort"], "sum")
		addDefaultArgument(query, "limit", call.Arguments["limit"], "5")
	case "list_namespace_operations":
		path = "/api/v1/namespaces"
		addArgument(query, "environment_id", call.Arguments["environment_id"])
		addArgument(query, "namespace", call.Arguments["namespace"])
		addDefaultArgument(query, "hours", call.Arguments["hours"], "24")
		addDefaultArgument(query, "sort", call.Arguments["sort"], "sum")
		addDefaultArgument(query, "direction", call.Arguments["direction"], "desc")
		addDefaultArgument(query, "limit", call.Arguments["limit"], "25")
		addArgument(query, "q", call.Arguments["q"])
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}

	var result map[string]any
	if err := client.Get(ctx, path, query, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func addArgument(query url.Values, key string, value any) {
	if value != nil {
		query.Set(key, numberString(value))
	}
}

func addDefaultArgument(query url.Values, key string, value any, fallback string) {
	if value == nil {
		query.Set(key, fallback)
		return
	}
	query.Set(key, numberString(value))
}

func numberString(value any) string {
	switch typed := value.(type) {
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}
