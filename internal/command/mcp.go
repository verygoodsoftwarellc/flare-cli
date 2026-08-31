package command

import (
	"context"
	"fmt"
	"net/url"
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
		Description: "List top operations for one Flare environment namespace.",
		InputSchema: objectSchema(map[string]any{
			"environment_id": positiveInteger,
			"namespace":      map[string]any{"type": "string", "enum": []string{"web", "job", "db", "http", "cache", "view"}},
			"hours":          map[string]any{"type": "integer", "enum": []int{1, 6, 24, 48, 168, 672}, "default": 24},
			"sort":           map[string]any{"type": "string", "enum": []string{"count", "sum", "avg", "error_rate", "impact"}, "default": "sum"},
			"direction":      map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "default": "desc"},
			"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25},
			"q":              map[string]any{"type": "string", "maxLength": 255},
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
		var response api.NamespaceResponse
		err := client.Get(ctx, "/api/v1/namespaces", query, &response)
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
	return hours == 1 || hours == 6 || hours == 24 || hours == 48 || hours == 168 || hours == 672
}

func validSort(sort string) bool {
	return sort == "count" || sort == "sum" || sort == "avg" || sort == "error_rate" || sort == "impact"
}

func validNamespace(namespace string) bool {
	return namespace == "web" || namespace == "job" || namespace == "db" || namespace == "http" || namespace == "cache" || namespace == "view"
}
