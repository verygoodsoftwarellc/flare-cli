package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
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
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
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
		if err := decodeStrict(request.Params, &call); err != nil {
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
		// The server body is passed through untouched: no boxing into
		// map[string]any and no re-encoding of large series payloads.
		response.Result = map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(result)}},
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
	text := map[string]any{"type": "string", "maxLength": maxFilterLength}
	hours := map[string]any{"type": "integer", "enum": allowedHours, "default": 24}
	namespace := map[string]any{"type": "string", "enum": namespaces}
	direction := map[string]any{"type": "string", "enum": directions, "default": "desc"}
	sortBy := func(values []string) map[string]any {
		return map[string]any{"type": "string", "enum": values, "default": "sum"}
	}
	limit := func(maximum, fallback int) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "maximum": maximum, "default": fallback}
	}
	pagination := map[string]any{"cursor": map[string]any{"type": "string"}, "limit": limit(100, 100)}
	scope := map[string]any{"environment_id": integer, "namespace": namespace, "hours": hours}
	selector := map[string]any{"service": text, "target": text, "operation": text}
	return []map[string]any{
		{
			"name": "list_organizations", "description": "List Flare organizations the user can access.",
			"inputSchema": objectSchema(pagination, nil), "annotations": readOnly,
		},
		{
			"name": "list_projects", "description": "List accessible Flare projects, optionally filtered by organization ID.",
			"inputSchema": objectSchema(mergeProperties(pagination, map[string]any{"organization_id": integer}), nil), "annotations": readOnly,
		},
		{
			"name": "list_environments", "description": "List accessible Flare environments, optionally filtered by project ID.",
			"inputSchema": objectSchema(mergeProperties(pagination, map[string]any{"project_id": integer}), nil), "annotations": readOnly,
		},
		{
			"name": "get_environment_overviews", "description": "Get bulk performance summaries for up to 25 Flare environments.",
			"inputSchema": objectSchema(map[string]any{
				"environment_ids": map[string]any{"type": "array", "items": integer, "minItems": 1, "maxItems": 25},
				"hours":           hours,
				"sort":            sortBy(sorts),
				"limit":           limit(25, 5),
			}, []string{"environment_ids"}), "annotations": readOnly,
		},
		{
			"name": "list_namespace_operations", "description": "Paginated, searchable, sortable operations table for one Flare environment namespace (aggregates only, no series). Each operation includes a selector (service/target/operation) that can be passed to get_operation_series.",
			"inputSchema": objectSchema(mergeProperties(scope, map[string]any{
				"sort":      sortBy(sorts),
				"direction": direction,
				"limit":     limit(100, 25),
				"q":         text,
			}), []string{"environment_id", "namespace"}), "annotations": readOnly,
		},
		{
			"name": "get_namespace_overview", "description": "Mirror of the Flare namespace page: stats and a time series for one environment namespace plus the top operations by sum and by count (default 10, max 25), each with its own series. " + windowHelp + " " + seriesHelp,
			"inputSchema": objectSchema(mergeProperties(scope, map[string]any{"limit": limit(maxSeriesLimit, 10)}), []string{"environment_id", "namespace"}), "annotations": readOnly,
		},
		{
			"name": "query_metrics", "description": "Generic Flare metrics query; the other metrics tools are presets of it. Flare stores count, total ms, and error count per (namespace, service, target, operation). Filter on any of the four dimensions, group_by any subset, rank groups by count, sum, avg, or error_rate (use min_count with rate sorts), and set series=true to attach series (limit <= 25 with series). " + windowHelp + " " + seriesHelp + " " + dimensionHelp + " Examples: error rate per status class for one controller (filter namespace=web + target, group_by operation); slowest hosts (namespace=http, group_by service, sort avg); jobs failing most (namespace=job, group_by target, sort error_rate, min_count 10). Totals across namespaces are not additive.",
			"inputSchema": objectSchema(mergeProperties(mergeProperties(scope, selector), map[string]any{
				"group_by":  map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": dimensions}, "maxItems": len(dimensions)},
				"sort":      sortBy(querySorts),
				"direction": direction,
				"limit":     limit(maxQueryLimit, 10),
				"series":    map[string]any{"type": "boolean", "default": false},
				"min_count": map[string]any{"type": "integer", "minimum": 0, "default": 0},
			}), []string{"environment_id"}), "annotations": readOnly,
		},
		{
			"name": "get_operation_series", "description": "Mirror of the Flare operation page: stats and a time series for one environment namespace, narrowed to one operation by the service/target/operation selector from list_namespace_operations or get_namespace_overview (omit all three for the namespace total). " + windowHelp + " " + seriesHelp,
			"inputSchema": objectSchema(mergeProperties(scope, selector), []string{"environment_id", "namespace"}), "annotations": readOnly,
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

func mergeProperties(left, right map[string]any) map[string]any {
	result := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		result[key] = value
	}
	return result
}

type catalogArguments struct {
	Cursor         string `json:"cursor"`
	Limit          int    `json:"limit"`
	OrganizationID *int64 `json:"organization_id,omitempty"`
	ProjectID      *int64 `json:"project_id,omitempty"`
}

type overviewArguments struct {
	EnvironmentIDs []int64 `json:"environment_ids"`
	Hours          int     `json:"hours"`
	Sort           string  `json:"sort"`
	Limit          int     `json:"limit"`
}

// The metrics tools decode into the same scope / selector structs the CLI
// binds its flags to, so both sides validate and encode identically.
type namespaceArguments struct {
	scope
	Sort      string `json:"sort"`
	Direction string `json:"direction"`
	Limit     int    `json:"limit"`
	Query     string `json:"q"`
}

type namespaceOverviewArguments struct {
	scope
	Limit int `json:"limit"`
}

type seriesArguments struct {
	scope
	selector
}

type queryArguments struct {
	scope
	selector
	GroupBy   []string `json:"group_by"`
	Sort      string   `json:"sort"`
	Direction string   `json:"direction"`
	Limit     int      `json:"limit"`
	Series    bool     `json:"series"`
	MinCount  int      `json:"min_count"`
}

func callMCPTool(ctx context.Context, client *api.Client, call toolCall) (json.RawMessage, error) {
	query := url.Values{}
	path := ""
	switch call.Name {
	case "list_organizations":
		path = "/api/v1/organizations"
		arguments := catalogArguments{}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.OrganizationID != nil || arguments.ProjectID != nil {
			return nil, fmt.Errorf("invalid organization arguments")
		}
		if err := addCatalogArguments(query, arguments); err != nil {
			return nil, err
		}
	case "list_projects":
		path = "/api/v1/projects"
		arguments := catalogArguments{}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.ProjectID != nil {
			return nil, fmt.Errorf("invalid project arguments")
		}
		if err := addCatalogArguments(query, arguments); err != nil {
			return nil, err
		}
		if arguments.OrganizationID != nil {
			query.Set("organization_id", strconv.FormatInt(*arguments.OrganizationID, 10))
		}
	case "list_environments":
		path = "/api/v1/environments"
		arguments := catalogArguments{}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.OrganizationID != nil {
			return nil, fmt.Errorf("invalid environment arguments")
		}
		if err := addCatalogArguments(query, arguments); err != nil {
			return nil, err
		}
		if arguments.ProjectID != nil {
			query.Set("project_id", strconv.FormatInt(*arguments.ProjectID, 10))
		}
	case "get_environment_overviews":
		path = "/api/v1/overviews"
		arguments := overviewArguments{Hours: 24, Sort: "sum", Limit: 5}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if len(arguments.EnvironmentIDs) == 0 || len(arguments.EnvironmentIDs) > 25 {
			return nil, fmt.Errorf("environment_ids is required")
		}
		if !slices.Contains(allowedHours, arguments.Hours) || !slices.Contains(sorts, arguments.Sort) || arguments.Limit < 1 || arguments.Limit > 25 {
			return nil, fmt.Errorf("invalid overview arguments")
		}
		values := make([]string, len(arguments.EnvironmentIDs))
		for index, id := range arguments.EnvironmentIDs {
			values[index] = strconv.FormatInt(id, 10)
		}
		query.Set("environment_ids", strings.Join(values, ","))
		query.Set("hours", strconv.Itoa(arguments.Hours))
		query.Set("sort", arguments.Sort)
		query.Set("limit", strconv.Itoa(arguments.Limit))
	case "list_namespace_operations":
		path = "/api/v1/namespaces"
		arguments := namespaceArguments{scope: scope{Hours: 24}, Sort: "sum", Direction: "desc", Limit: 25}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.validate(true) != nil || !slices.Contains(sorts, arguments.Sort) || !slices.Contains(directions, arguments.Direction) ||
			arguments.Limit < 1 || arguments.Limit > 100 || len(arguments.Query) > maxFilterLength {
			return nil, fmt.Errorf("invalid namespace arguments")
		}
		query = arguments.values()
		query.Set("sort", arguments.Sort)
		query.Set("direction", arguments.Direction)
		query.Set("limit", strconv.Itoa(arguments.Limit))
		if arguments.Query != "" {
			query.Set("q", arguments.Query)
		}
	case "get_namespace_overview":
		path = "/api/v1/namespaces/overview"
		arguments := namespaceOverviewArguments{scope: scope{Hours: 24}, Limit: 10}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.validate(true) != nil || arguments.Limit < 1 || arguments.Limit > maxSeriesLimit {
			return nil, fmt.Errorf("invalid namespace overview arguments")
		}
		query = arguments.values()
		query.Set("limit", strconv.Itoa(arguments.Limit))
	case "query_metrics":
		path = "/api/v1/metrics/query"
		arguments := queryArguments{scope: scope{Hours: 24}, Sort: "sum", Direction: "desc", Limit: 10}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		maxLimit := maxQueryLimit
		if arguments.Series {
			maxLimit = maxSeriesLimit
		}
		if arguments.scope.validate(false) != nil || arguments.selector.validate() != nil ||
			!slices.Contains(querySorts, arguments.Sort) || !slices.Contains(directions, arguments.Direction) ||
			arguments.Limit < 1 || arguments.Limit > maxLimit || arguments.MinCount < 0 || !validGroupBy(arguments.GroupBy) {
			return nil, fmt.Errorf("invalid query arguments")
		}
		query = arguments.values()
		arguments.selector.apply(query)
		query.Set("sort", arguments.Sort)
		query.Set("direction", arguments.Direction)
		query.Set("limit", strconv.Itoa(arguments.Limit))
		if len(arguments.GroupBy) > 0 {
			query.Set("group_by", strings.Join(arguments.GroupBy, ","))
		}
		if arguments.Series {
			query.Set("series", "true")
		}
		if arguments.MinCount > 0 {
			query.Set("min_count", strconv.Itoa(arguments.MinCount))
		}
	case "get_operation_series":
		path = "/api/v1/series"
		arguments := seriesArguments{scope: scope{Hours: 24}}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.scope.validate(true) != nil || arguments.selector.validate() != nil {
			return nil, fmt.Errorf("invalid series arguments")
		}
		query = arguments.values()
		arguments.selector.apply(query)
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}

	var result json.RawMessage
	if err := client.Get(ctx, path, query, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func addCatalogArguments(query url.Values, arguments catalogArguments) error {
	if arguments.Limit == 0 {
		arguments.Limit = 100
	}
	if arguments.Limit < 1 || arguments.Limit > 100 {
		return fmt.Errorf("limit must be between 1 and 100")
	}
	query.Set("limit", strconv.Itoa(arguments.Limit))
	if arguments.Cursor != "" {
		query.Set("cursor", arguments.Cursor)
	}
	return nil
}

func decodeStrictArguments(raw json.RawMessage, output any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return decodeStrict(raw, output)
}

func decodeStrict(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// validGroupBy accepts a subset of the dimensions with no repeats.
func validGroupBy(values []string) bool {
	for index, value := range values {
		if !slices.Contains(dimensions, value) || slices.Contains(values[:index], value) {
			return false
		}
	}
	return true
}
