package command

import (
	"bufio"
	"bytes"
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
	pagination := map[string]any{
		"cursor": map[string]any{"type": "string"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 100},
	}
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
				"hours":           map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
				"sort":            map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate", "impact"}, "default": "sum"},
				"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 25, "default": 5},
			}, []string{"environment_ids"}), "annotations": readOnly,
		},
		{
			"name": "list_namespace_operations", "description": "Paginated, searchable, sortable operations table for one Flare environment namespace (aggregates only, no series). Each operation includes a selector (service/target/operation) that can be passed to get_operation_series.",
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
		{
			"name": "get_namespace_overview", "description": "Mirror of the Flare namespace page: stats and a time series for one environment namespace plus the top operations by sum and by count (default 10, max 25), each with its own series. hours 1 or 6 return minute buckets over a rolling window ending now (use hours=1 for the last 60 minutes), 24 to 168 return hour buckets, 672 returns day buckets. Every series is {count, sum, error_count} arrays aligned to the shared buckets array (per-bucket avg = sum/count); data_through is the latest bucket with data.",
			"inputSchema": objectSchema(map[string]any{
				"environment_id": integer,
				"namespace":      map[string]any{"type": "string", "enum": []string{"web", "job", "db", "http", "cache", "view"}},
				"hours":          map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
				"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 25, "default": 10},
			}, []string{"environment_id", "namespace"}), "annotations": readOnly,
		},
		{
			"name": "query_metrics", "description": "Generic Flare metrics query; the other metrics tools are presets of it. Flare stores count, total ms, and error count per (namespace, service, target, operation) at minute/hour/day grain. Filter on any of the four dimensions, group_by any subset, rank groups by count, sum, avg, or error_rate (use min_count with rate sorts), and set series=true to attach {count, sum, error_count} arrays aligned to the shared buckets array (limit <= 25 with series). Dimension meanings by namespace: web — service: framework, target: controller#action, operation: HTTP status class (2xx..5xx); job — service: backend (solid_queue, sidekiq), target: job class, operation: perform; db — service: adapter, target: table, operation: SQL verb; http — service: remote host, target: method and path, operation: HTTP status class; cache — service: store, operation: read.hit/read.miss/write/delete; view — service: renderer, target: template path, operation: template/partial/layout/collection. Examples: error rate per status class for one controller (filter namespace=web + target, group_by operation); slowest hosts (namespace=http, group_by service, sort avg); jobs failing most (namespace=job, group_by target, sort error_rate, min_count 10). Totals across namespaces are not additive.",
			"inputSchema": objectSchema(map[string]any{
				"environment_id": integer,
				"hours":          map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
				"namespace":      map[string]any{"type": "string", "enum": []string{"web", "job", "db", "http", "cache", "view"}},
				"service":        map[string]any{"type": "string", "maxLength": 255},
				"target":         map[string]any{"type": "string", "maxLength": 255},
				"operation":      map[string]any{"type": "string", "maxLength": 255},
				"group_by":       map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"namespace", "service", "target", "operation"}}, "maxItems": 4},
				"sort":           map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate"}, "default": "sum"},
				"direction":      map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "default": "desc"},
				"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 10},
				"series":         map[string]any{"type": "boolean", "default": false},
				"min_count":      map[string]any{"type": "integer", "minimum": 0, "default": 0},
			}, []string{"environment_id"}), "annotations": readOnly,
		},
		{
			"name": "get_operation_series", "description": "Mirror of the Flare operation page: stats and a time series for one environment namespace, narrowed to one operation by the service/target/operation selector from list_namespace_operations or get_namespace_overview (omit all three for the namespace total). Same windows, buckets, and series encoding as get_namespace_overview.",
			"inputSchema": objectSchema(map[string]any{
				"environment_id": integer,
				"namespace":      map[string]any{"type": "string", "enum": []string{"web", "job", "db", "http", "cache", "view"}},
				"hours":          map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
				"service":        map[string]any{"type": "string", "maxLength": 255},
				"target":         map[string]any{"type": "string", "maxLength": 255},
				"operation":      map[string]any{"type": "string", "maxLength": 255},
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

type namespaceArguments struct {
	EnvironmentID int64  `json:"environment_id"`
	Namespace     string `json:"namespace"`
	Hours         int    `json:"hours"`
	Sort          string `json:"sort"`
	Direction     string `json:"direction"`
	Limit         int    `json:"limit"`
	Query         string `json:"q"`
}

type namespaceOverviewArguments struct {
	EnvironmentID int64  `json:"environment_id"`
	Namespace     string `json:"namespace"`
	Hours         int    `json:"hours"`
	Limit         int    `json:"limit"`
}

type queryArguments struct {
	EnvironmentID int64    `json:"environment_id"`
	Hours         int      `json:"hours"`
	Namespace     string   `json:"namespace"`
	Service       string   `json:"service"`
	Target        string   `json:"target"`
	Operation     string   `json:"operation"`
	GroupBy       []string `json:"group_by"`
	Sort          string   `json:"sort"`
	Direction     string   `json:"direction"`
	Limit         int      `json:"limit"`
	Series        bool     `json:"series"`
	MinCount      int      `json:"min_count"`
}

type seriesArguments struct {
	EnvironmentID int64  `json:"environment_id"`
	Namespace     string `json:"namespace"`
	Hours         int    `json:"hours"`
	Service       string `json:"service"`
	Target        string `json:"target"`
	Operation     string `json:"operation"`
}

func callMCPTool(ctx context.Context, client *api.Client, call toolCall) (map[string]any, error) {
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
		if !validHours(arguments.Hours) || !validSort(arguments.Sort) || arguments.Limit < 1 || arguments.Limit > 25 {
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
		arguments := namespaceArguments{Hours: 24, Sort: "sum", Direction: "desc", Limit: 25}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.EnvironmentID <= 0 || !validNamespace(arguments.Namespace) || !validHours(arguments.Hours) ||
			!validSort(arguments.Sort) || (arguments.Direction != "asc" && arguments.Direction != "desc") ||
			arguments.Limit < 1 || arguments.Limit > 100 || len(arguments.Query) > 255 {
			return nil, fmt.Errorf("invalid namespace arguments")
		}
		query.Set("environment_id", strconv.FormatInt(arguments.EnvironmentID, 10))
		query.Set("namespace", arguments.Namespace)
		query.Set("hours", strconv.Itoa(arguments.Hours))
		query.Set("sort", arguments.Sort)
		query.Set("direction", arguments.Direction)
		query.Set("limit", strconv.Itoa(arguments.Limit))
		if arguments.Query != "" {
			query.Set("q", arguments.Query)
		}
	case "get_namespace_overview":
		path = "/api/v1/namespaces/overview"
		arguments := namespaceOverviewArguments{Hours: 24, Limit: 10}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.EnvironmentID <= 0 || !validNamespace(arguments.Namespace) || !validHours(arguments.Hours) ||
			arguments.Limit < 1 || arguments.Limit > 25 {
			return nil, fmt.Errorf("invalid namespace overview arguments")
		}
		query.Set("environment_id", strconv.FormatInt(arguments.EnvironmentID, 10))
		query.Set("namespace", arguments.Namespace)
		query.Set("hours", strconv.Itoa(arguments.Hours))
		query.Set("limit", strconv.Itoa(arguments.Limit))
	case "query_metrics":
		path = "/api/v1/metrics/query"
		arguments := queryArguments{Hours: 24, Sort: "sum", Direction: "desc", Limit: 10}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		maxLimit := 100
		if arguments.Series {
			maxLimit = 25
		}
		if arguments.EnvironmentID <= 0 || !validHours(arguments.Hours) || (arguments.Namespace != "" && !validNamespace(arguments.Namespace)) ||
			!validQuerySort(arguments.Sort) || (arguments.Direction != "asc" && arguments.Direction != "desc") ||
			arguments.Limit < 1 || arguments.Limit > maxLimit || arguments.MinCount < 0 ||
			len(arguments.Service) > 255 || len(arguments.Target) > 255 || len(arguments.Operation) > 255 || !validGroupBy(arguments.GroupBy) {
			return nil, fmt.Errorf("invalid query arguments")
		}
		query.Set("environment_id", strconv.FormatInt(arguments.EnvironmentID, 10))
		query.Set("hours", strconv.Itoa(arguments.Hours))
		query.Set("sort", arguments.Sort)
		query.Set("direction", arguments.Direction)
		query.Set("limit", strconv.Itoa(arguments.Limit))
		if arguments.Namespace != "" {
			query.Set("namespace", arguments.Namespace)
		}
		addSelector(query, arguments.Service, arguments.Target, arguments.Operation)
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
		arguments := seriesArguments{Hours: 24}
		if err := decodeStrictArguments(call.Arguments, &arguments); err != nil {
			return nil, err
		}
		if arguments.EnvironmentID <= 0 || !validNamespace(arguments.Namespace) || !validHours(arguments.Hours) ||
			len(arguments.Service) > 255 || len(arguments.Target) > 255 || len(arguments.Operation) > 255 {
			return nil, fmt.Errorf("invalid series arguments")
		}
		query.Set("environment_id", strconv.FormatInt(arguments.EnvironmentID, 10))
		query.Set("namespace", arguments.Namespace)
		query.Set("hours", strconv.Itoa(arguments.Hours))
		addSelector(query, arguments.Service, arguments.Target, arguments.Operation)
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}

	var result map[string]any
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

func validHours(hours int) bool {
	return hours == 1 || hours == 6 || hours == 24 || hours == 48 || hours == 168 || hours == 672
}

func validSort(sort string) bool {
	return sort == "count" || sort == "sum" || sort == "avg" || sort == "error_rate" || sort == "impact"
}

func validQuerySort(sort string) bool {
	return sort == "count" || sort == "sum" || sort == "avg" || sort == "error_rate"
}

func validGroupBy(dimensions []string) bool {
	seen := map[string]bool{}
	for _, dimension := range dimensions {
		if dimension != "namespace" && dimension != "service" && dimension != "target" && dimension != "operation" {
			return false
		}
		if seen[dimension] {
			return false
		}
		seen[dimension] = true
	}
	return true
}

func validNamespace(namespace string) bool {
	return namespace == "web" || namespace == "job" || namespace == "db" || namespace == "http" || namespace == "cache" || namespace == "view"
}
