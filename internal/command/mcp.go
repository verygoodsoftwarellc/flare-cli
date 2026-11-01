package command

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/version"
)

type listOrganizationsInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type listProjectsInput struct {
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	OrganizationID *int64 `json:"organization_id,omitempty"`
}

type listEnvironmentsInput struct {
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	ProjectID *int64 `json:"project_id,omitempty"`
}

type overviewInput struct {
	EnvironmentIDs []int64 `json:"environment_ids"`
	Hours          int     `json:"hours,omitempty"`
	Sort           string  `json:"sort,omitempty"`
	Limit          int     `json:"limit,omitempty"`
}

type namespaceInput struct {
	EnvironmentID int64  `json:"environment_id"`
	Namespace     string `json:"namespace"`
	Hours         int    `json:"hours,omitempty"`
	Sort          string `json:"sort,omitempty"`
	Direction     string `json:"direction,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Query         string `json:"q,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

// The series-shaped tools decode into the same scope / selector structs the
// CLI binds its flags to, so both sides validate and encode identically.
type namespaceOverviewInput struct {
	scope
	Limit int `json:"limit,omitempty"`
}

type seriesInput struct {
	scope
	selector
}

type queryInput struct {
	scope
	selector
	GroupBy   []string `json:"group_by,omitempty"`
	Sort      string   `json:"sort,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Series    bool     `json:"series,omitempty"`
	MinCount  int      `json:"min_count,omitempty"`
}

func newMCPCommand(options *rootOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Run a read-only MCP server over stdio",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := options.client()
			if err != nil {
				return err
			}
			return newMCPServer(client).Run(command.Context(), &mcp.StdioTransport{})
		},
	}
	command.AddCommand(newMCPInstallCommand(options), newMCPConfigCommand(options))
	return command
}

func newMCPServer(client *api.Client) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "flare", Version: version.Current()}, nil)
	annotations := readOnlyToolAnnotations()
	positiveInteger := map[string]any{"type": "integer", "minimum": 1}
	pagination := map[string]any{
		"cursor": map[string]any{"type": "string"},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 100},
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_organizations",
		Description: "List Flare organizations the user can access.",
		InputSchema: objectSchema(pagination, nil),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listOrganizationsInput) (*mcp.CallToolResult, api.OrganizationsResponse, error) {
		query, err := catalogQuery(input.Cursor, input.Limit)
		if err != nil {
			return nil, api.OrganizationsResponse{}, err
		}
		var response api.OrganizationsResponse
		err = client.Get(ctx, "/api/v1/organizations", query, &response)
		return nil, response, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Description: "List accessible Flare projects, optionally filtered by organization ID.",
		InputSchema: objectSchema(mergeProperties(pagination, map[string]any{"organization_id": positiveInteger}), nil),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listProjectsInput) (*mcp.CallToolResult, api.ProjectsResponse, error) {
		query, err := catalogQuery(input.Cursor, input.Limit)
		if err != nil {
			return nil, api.ProjectsResponse{}, err
		}
		if input.OrganizationID != nil {
			if *input.OrganizationID <= 0 {
				return nil, api.ProjectsResponse{}, fmt.Errorf("organization_id must be positive")
			}
			query.Set("organization_id", strconv.FormatInt(*input.OrganizationID, 10))
		}
		var response api.ProjectsResponse
		err = client.Get(ctx, "/api/v1/projects", query, &response)
		return nil, response, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_environments",
		Description: "List accessible Flare environments, optionally filtered by project ID.",
		InputSchema: objectSchema(mergeProperties(pagination, map[string]any{"project_id": positiveInteger}), nil),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listEnvironmentsInput) (*mcp.CallToolResult, api.EnvironmentsResponse, error) {
		query, err := catalogQuery(input.Cursor, input.Limit)
		if err != nil {
			return nil, api.EnvironmentsResponse{}, err
		}
		if input.ProjectID != nil {
			if *input.ProjectID <= 0 {
				return nil, api.EnvironmentsResponse{}, fmt.Errorf("project_id must be positive")
			}
			query.Set("project_id", strconv.FormatInt(*input.ProjectID, 10))
		}
		var response api.EnvironmentsResponse
		err = client.Get(ctx, "/api/v1/environments", query, &response)
		return nil, response, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_environment_overviews",
		Description: "Get bulk performance summaries for up to 25 Flare environments.",
		InputSchema: objectSchema(map[string]any{
			"environment_ids": map[string]any{"type": "array", "items": positiveInteger, "minItems": 1, "maxItems": 25, "uniqueItems": true},
			"hours":           map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
			"sort":            map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate", "impact"}, "default": "sum"},
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 25, "default": 5},
		}, []string{"environment_ids"}),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input overviewInput) (*mcp.CallToolResult, api.OverviewsResponse, error) {
		if err := input.setDefaultsAndValidate(); err != nil {
			return nil, api.OverviewsResponse{}, err
		}
		values := make([]string, len(input.EnvironmentIDs))
		for index, id := range input.EnvironmentIDs {
			values[index] = strconv.FormatInt(id, 10)
		}
		query := url.Values{
			"environment_ids": {strings.Join(values, ",")},
			"hours":           {strconv.Itoa(input.Hours)},
			"sort":            {input.Sort},
			"limit":           {strconv.Itoa(input.Limit)},
		}
		var response api.OverviewsResponse
		err := client.Get(ctx, "/api/v1/overviews", query, &response)
		return nil, response, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_namespace_operations",
		Description: "List top operations for one Flare environment namespace. For the next page, pass pagination.next as cursor with the same other arguments.",
		InputSchema: objectSchema(map[string]any{
			"environment_id": positiveInteger,
			"namespace":      map[string]any{"type": "string", "enum": []string{"web", "job", "db", "http", "cache", "view"}},
			"hours":          map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
			"sort":           map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate", "impact"}, "default": "sum"},
			"direction":      map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "default": "desc"},
			"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25},
			"q":              map[string]any{"type": "string", "maxLength": 255},
			"cursor":         pagination["cursor"],
		}, []string{"environment_id", "namespace"}),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input namespaceInput) (*mcp.CallToolResult, api.NamespaceResponse, error) {
		if err := input.setDefaultsAndValidate(); err != nil {
			return nil, api.NamespaceResponse{}, err
		}
		query := url.Values{
			"environment_id": {strconv.FormatInt(input.EnvironmentID, 10)},
			"namespace":      {input.Namespace},
			"hours":          {strconv.Itoa(input.Hours)},
			"sort":           {input.Sort},
			"direction":      {input.Direction},
			"limit":          {strconv.Itoa(input.Limit)},
		}
		if input.Query != "" {
			query.Set("q", input.Query)
		}
		if input.Cursor != "" {
			query.Set("cursor", input.Cursor)
		}
		var response api.NamespaceResponse
		err := client.Get(ctx, "/api/v1/namespaces", query, &response)
		return nil, response, err
	})

	text := map[string]any{"type": "string", "maxLength": maxFilterLength}
	hours := map[string]any{"type": "integer", "enum": allowedHours, "default": 24}
	scopeProperties := map[string]any{
		"environment_id": positiveInteger,
		"namespace":      map[string]any{"type": "string", "enum": namespaces},
		"hours":          hours,
	}
	selectorProperties := map[string]any{"service": text, "target": text, "operation": text}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_namespace_overview",
		Description: "Mirror of the Flare namespace page: stats and a time series for one environment namespace plus the top operations by sum and by count (default 10, max 25), each with its own series. " + windowHelp + " " + seriesHelp,
		InputSchema: objectSchema(mergeProperties(scopeProperties, map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": maxSeriesLimit, "default": 10},
		}), []string{"environment_id", "namespace"}),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input namespaceOverviewInput) (*mcp.CallToolResult, api.NamespaceOverviewResponse, error) {
		input.setDefaults()
		if input.Limit == 0 {
			input.Limit = 10
		}
		if err := input.validate(true); err != nil {
			return nil, api.NamespaceOverviewResponse{}, err
		}
		if input.Limit < 1 || input.Limit > maxSeriesLimit {
			return nil, api.NamespaceOverviewResponse{}, fmt.Errorf("limit must be between 1 and %d", maxSeriesLimit)
		}
		query := input.values()
		query.Set("limit", strconv.Itoa(input.Limit))
		var response api.NamespaceOverviewResponse
		err := client.Get(ctx, "/api/v1/namespaces/overview", query, &response)
		return nil, response, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "query_metrics",
		Description: "Generic Flare metrics query; the other metrics tools are presets of it. Flare stores count, total ms, and error count per (namespace, service, target, operation). Filter on any of the four dimensions, group_by any subset, rank groups by count, sum, avg, or error_rate (use min_count with rate sorts), and set series=true to attach series (limit <= 25 with series). " + windowHelp + " " + seriesHelp + " " + dimensionHelp + " Examples: error rate per status class for one controller (filter namespace=web + target, group_by operation); slowest hosts (namespace=http, group_by service, sort avg); jobs failing most (namespace=job, group_by target, sort error_rate, min_count 10). Totals across namespaces are not additive.",
		InputSchema: objectSchema(mergeProperties(mergeProperties(scopeProperties, selectorProperties), map[string]any{
			"group_by":  map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": dimensions}, "maxItems": len(dimensions), "uniqueItems": true},
			"sort":      map[string]any{"type": "string", "enum": querySorts, "default": "sum"},
			"direction": map[string]any{"type": "string", "enum": directions, "default": "desc"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": maxQueryLimit, "default": 10},
			"series":    map[string]any{"type": "boolean", "default": false},
			"min_count": map[string]any{"type": "integer", "minimum": 0, "default": 0},
		}), []string{"environment_id"}),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input queryInput) (*mcp.CallToolResult, api.QueryResponse, error) {
		input.setDefaults()
		if input.Sort == "" {
			input.Sort = "sum"
		}
		if input.Direction == "" {
			input.Direction = "desc"
		}
		if input.Limit == 0 {
			input.Limit = 10
		}
		if err := errors.Join(input.scope.validate(false), input.selector.validate()); err != nil {
			return nil, api.QueryResponse{}, err
		}
		maxLimit := maxQueryLimit
		if input.Series {
			maxLimit = maxSeriesLimit
		}
		if !slices.Contains(querySorts, input.Sort) || !slices.Contains(directions, input.Direction) ||
			input.Limit < 1 || input.Limit > maxLimit || input.MinCount < 0 || !validGroupBy(input.GroupBy) {
			return nil, api.QueryResponse{}, fmt.Errorf("invalid query arguments")
		}
		query := input.values()
		input.selector.apply(query)
		query.Set("sort", input.Sort)
		query.Set("direction", input.Direction)
		query.Set("limit", strconv.Itoa(input.Limit))
		if len(input.GroupBy) > 0 {
			query.Set("group_by", strings.Join(input.GroupBy, ","))
		}
		if input.Series {
			query.Set("series", "true")
		}
		if input.MinCount > 0 {
			query.Set("min_count", strconv.Itoa(input.MinCount))
		}
		var response api.QueryResponse
		err := client.Get(ctx, "/api/v1/metrics/query", query, &response)
		return nil, response, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_operation_series",
		Description: "Mirror of the Flare operation page: stats and a time series for one environment namespace, narrowed to one operation by the service/target/operation selector from list_namespace_operations or get_namespace_overview (omit all three for the namespace total). " + windowHelp + " " + seriesHelp,
		InputSchema: objectSchema(mergeProperties(scopeProperties, selectorProperties), []string{"environment_id", "namespace"}),
		Annotations: annotations,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input seriesInput) (*mcp.CallToolResult, api.OperationSeriesResponse, error) {
		input.setDefaults()
		if err := errors.Join(input.scope.validate(true), input.selector.validate()); err != nil {
			return nil, api.OperationSeriesResponse{}, err
		}
		query := input.values()
		input.selector.apply(query)
		var response api.OperationSeriesResponse
		err := client.Get(ctx, "/api/v1/series", query, &response)
		return nil, response, err
	})

	return server
}

func readOnlyToolAnnotations() *mcp.ToolAnnotations {
	falseValue := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &falseValue,
		IdempotentHint:  true,
		OpenWorldHint:   &falseValue,
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

func catalogQuery(cursor string, limit int) (url.Values, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("limit must be between 1 and 100")
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	return query, nil
}

func (input *overviewInput) setDefaultsAndValidate() error {
	if input.Hours == 0 {
		input.Hours = 24
	}
	if input.Sort == "" {
		input.Sort = "sum"
	}
	if input.Limit == 0 {
		input.Limit = 5
	}
	if len(input.EnvironmentIDs) == 0 || len(input.EnvironmentIDs) > 25 {
		return fmt.Errorf("environment_ids must contain between 1 and 25 IDs")
	}
	seen := make(map[int64]struct{}, len(input.EnvironmentIDs))
	for _, id := range input.EnvironmentIDs {
		if id <= 0 {
			return fmt.Errorf("environment_ids must contain only positive IDs")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("environment_ids must not contain duplicates")
		}
		seen[id] = struct{}{}
	}
	if !validHours(input.Hours) || !validSort(input.Sort) || input.Limit < 1 || input.Limit > 25 {
		return fmt.Errorf("invalid overview arguments")
	}
	return nil
}

func (input *namespaceInput) setDefaultsAndValidate() error {
	if input.Hours == 0 {
		input.Hours = 24
	}
	if input.Sort == "" {
		input.Sort = "sum"
	}
	if input.Direction == "" {
		input.Direction = "desc"
	}
	if input.Limit == 0 {
		input.Limit = 25
	}
	if input.EnvironmentID <= 0 || !validNamespace(input.Namespace) || !validHours(input.Hours) ||
		!validSort(input.Sort) || (input.Direction != "asc" && input.Direction != "desc") ||
		input.Limit < 1 || input.Limit > 100 || utf8.RuneCountInString(input.Query) > 255 {
		return fmt.Errorf("invalid namespace arguments")
	}
	return nil
}

func validHours(hours int) bool {
	return slices.Contains(allowedHours, hours)
}

func validSort(sort string) bool {
	return slices.Contains(sorts, sort)
}

func validNamespace(namespace string) bool {
	return slices.Contains(namespaces, namespace)
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
