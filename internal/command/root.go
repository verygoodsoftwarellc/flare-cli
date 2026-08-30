package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/auth"
	"github.com/verygoodsoftwarellc/flare-cli/internal/config"
)

type rootOptions struct {
	apiURL  string
	json    bool
	verbose bool
	output  io.Writer
	errors  io.Writer
	input   io.Reader
}

func Execute() error {
	return newRootCommand().Execute()
}

func newRootCommand() *cobra.Command {
	options := &rootOptions{output: os.Stdout, errors: os.Stderr, input: os.Stdin}
	command := &cobra.Command{
		Use:           "flare",
		Short:         "Inspect application performance in Flare",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.PersistentFlags().StringVar(&options.apiURL, "api-url", "", "Flare base URL (or FLARE_API_URL)")
	command.PersistentFlags().BoolVar(&options.json, "json", false, "print raw JSON")
	command.PersistentFlags().BoolVar(&options.verbose, "verbose", false, "print request metadata")
	command.AddCommand(
		newAuthCommand(options),
		newOrganizationCommand(options),
		newProjectCommand(options),
		newEnvironmentCommand(options),
		newMetricsCommand(options),
		newMCPCommand(options),
	)
	return command
}

func (options *rootOptions) config() (*config.Config, error) {
	return config.Load(options.apiURL)
}

func (options *rootOptions) client() (*api.Client, error) {
	settings, err := options.config()
	if err != nil {
		return nil, err
	}
	token, err := settings.TokenValue()
	if err != nil {
		return nil, err
	}
	client := api.New(settings.APIURL, token)
	client.Verbose = options.verbose
	client.ErrWriter = options.errors
	return client, nil
}

func newAuthCommand(options *rootOptions) *cobra.Command {
	command := &cobra.Command{Use: "auth", Short: "Manage authentication"}
	var withToken bool
	login := &cobra.Command{
		Use:   "login",
		Short: "Authenticate in your browser",
		RunE: func(command *cobra.Command, _ []string) error {
			settings, err := options.config()
			if err != nil {
				return err
			}
			token := ""
			if withToken {
				value, err := io.ReadAll(io.LimitReader(options.input, 1024))
				if err != nil {
					return fmt.Errorf("read token from stdin: %w", err)
				}
				token = strings.TrimSpace(string(value))
				if !strings.HasPrefix(token, "flare_pat_") {
					return errors.New("stdin did not contain a Flare personal access token")
				}
			} else {
				ctx, cancel := context.WithTimeout(command.Context(), 3*time.Minute)
				defer cancel()
				fmt.Fprintln(options.errors, "Opening Flare in your browser…")
				result, err := auth.Login(ctx, settings.APIURL, nil)
				if err != nil {
					return err
				}
				token = result.Token
			}
			fallback, err := settings.StoreToken(token)
			if err != nil {
				return err
			}
			if fallback {
				path, _ := config.Path()
				fmt.Fprintf(options.errors, "warning: OS credential store unavailable; token saved mode 0600 in %s\n", path)
			}
			fmt.Fprintf(options.output, "Authenticated with %s\n", settings.APIURL)
			return nil
		},
	}
	login.Flags().BoolVar(&withToken, "with-token", false, "read a personal access token from stdin")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show authentication status",
		RunE: func(command *cobra.Command, _ []string) error {
			settings, err := options.config()
			if err != nil {
				return err
			}
			token, err := settings.TokenValue()
			if err != nil {
				return err
			}
			client := api.New(settings.APIURL, token)
			client.Verbose = options.verbose
			client.ErrWriter = options.errors
			if err := verifyAuthentication(command.Context(), client); err != nil {
				return err
			}
			fmt.Fprintf(options.output, "Authenticated with %s\n", settings.APIURL)
			return nil
		},
	}
	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		RunE: func(_ *cobra.Command, _ []string) error {
			settings, err := options.config()
			if err != nil {
				return err
			}
			if err := settings.DeleteToken(); err != nil {
				return err
			}
			fmt.Fprintln(options.output, "Logged out")
			return nil
		},
	}
	command.AddCommand(login, status, logout)
	return command
}

func verifyAuthentication(ctx context.Context, client *api.Client) error {
	var response api.OrganizationsResponse
	err := client.Get(ctx, "/api/v1/organizations", url.Values{"limit": {"1"}}, &response)
	var apiError *api.APIError
	if errors.As(err, &apiError) && apiError.Status == http.StatusForbidden {
		return nil
	}
	return err
}

func newOrganizationCommand(options *rootOptions) *cobra.Command {
	parent := &cobra.Command{Use: "org", Short: "Work with organizations"}
	parent.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List accessible organizations",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := options.client()
			if err != nil {
				return err
			}
			response, err := allOrganizations(command.Context(), client)
			if err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			return writeTable(options.output, []string{"ID", "NAME"}, organizationRows(response.Organizations))
		},
	})
	return parent
}

func newProjectCommand(options *rootOptions) *cobra.Command {
	var organizationID int64
	parent := &cobra.Command{Use: "project", Short: "Work with projects"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List accessible projects",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := options.client()
			if err != nil {
				return err
			}
			query := url.Values{"limit": {"100"}}
			api.AddInt(query, "organization_id", organizationID)
			response, err := allProjects(command.Context(), client, query)
			if err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			rows := make([][]string, 0, len(response.Projects))
			for _, project := range response.Projects {
				rows = append(rows, []string{fmt.Sprint(project.ID), fmt.Sprint(project.OrganizationID), project.Name})
			}
			return writeTable(options.output, []string{"ID", "ORG", "NAME"}, rows)
		},
	}
	list.Flags().Int64Var(&organizationID, "org", 0, "filter by organization ID")
	parent.AddCommand(list)
	return parent
}

func newEnvironmentCommand(options *rootOptions) *cobra.Command {
	var projectID int64
	parent := &cobra.Command{Use: "environment", Aliases: []string{"env"}, Short: "Work with environments"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List accessible environments",
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := options.client()
			if err != nil {
				return err
			}
			query := url.Values{"limit": {"100"}}
			api.AddInt(query, "project_id", projectID)
			response, err := allEnvironments(command.Context(), client, query)
			if err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			rows := make([][]string, 0, len(response.Environments))
			for _, environment := range response.Environments {
				rows = append(rows, []string{fmt.Sprint(environment.ID), fmt.Sprint(environment.ProjectID), environment.Name})
			}
			return writeTable(options.output, []string{"ID", "PROJECT", "NAME"}, rows)
		},
	}
	list.Flags().Int64Var(&projectID, "project", 0, "filter by project ID")
	parent.AddCommand(list)
	return parent
}

func newMetricsCommand(options *rootOptions) *cobra.Command {
	parent := &cobra.Command{Use: "metrics", Short: "Inspect performance metrics"}
	parent.AddCommand(newOverviewCommand(options), newNamespaceCommand(options))
	return parent
}

func newOverviewCommand(options *rootOptions) *cobra.Command {
	var environmentIDs []int64
	var hours, limit int
	var sort string
	command := &cobra.Command{
		Use:   "overview",
		Short: "Summarize one or more environments",
		RunE: func(command *cobra.Command, _ []string) error {
			if len(environmentIDs) == 0 {
				return errors.New("at least one --environment is required")
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			ids := make([]string, len(environmentIDs))
			for index, id := range environmentIDs {
				ids[index] = strconv.FormatInt(id, 10)
			}
			query := url.Values{
				"environment_ids": {strings.Join(ids, ",")},
				"hours":           {strconv.Itoa(hours)},
				"sort":            {sort},
				"limit":           {strconv.Itoa(limit)},
			}
			var response api.OverviewsResponse
			if err := client.Get(command.Context(), "/api/v1/overviews", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			rows := [][]string{}
			for _, overview := range response.Overviews {
				for _, namespace := range overview.Namespaces {
					rows = append(rows, []string{
						overview.Environment.Name, namespace.Namespace, fmt.Sprint(namespace.Count),
						formatMilliseconds(namespace.Sum), formatMilliseconds(int64(namespace.Avg)),
						fmt.Sprintf("%.2f%%", namespace.ErrorRate),
					})
				}
			}
			return writeTable(options.output, []string{"ENVIRONMENT", "NAMESPACE", "COUNT", "SUM", "AVG", "ERROR"}, rows)
		},
	}
	command.Flags().Int64SliceVarP(&environmentIDs, "environment", "e", nil, "environment ID (repeatable)")
	command.Flags().IntVar(&hours, "hours", 24, "time window: 1, 6, 24, 48, 168, or 672")
	command.Flags().StringVar(&sort, "sort", "sum", "rank operations by count, sum, avg, error_rate, or impact")
	command.Flags().IntVar(&limit, "limit", 5, "top operations per namespace")
	return command
}

func newNamespaceCommand(options *rootOptions) *cobra.Command {
	var environmentID int64
	var hours, limit int
	var namespace, sort, direction, search string
	command := &cobra.Command{
		Use:   "namespace",
		Short: "List operations in a namespace",
		RunE: func(command *cobra.Command, _ []string) error {
			if environmentID == 0 || namespace == "" {
				return errors.New("--environment and --namespace are required")
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			query := url.Values{
				"environment_id": {strconv.FormatInt(environmentID, 10)}, "namespace": {namespace},
				"hours": {strconv.Itoa(hours)}, "sort": {sort}, "direction": {direction},
				"limit": {strconv.Itoa(limit)}, "q": {search},
			}
			var response api.NamespaceResponse
			if err := client.Get(command.Context(), "/api/v1/namespaces", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			rows := make([][]string, 0, len(response.Operations))
			for _, operation := range response.Operations {
				rows = append(rows, []string{operation.Operation, fmt.Sprint(operation.Count), formatMilliseconds(operation.Sum), formatMilliseconds(int64(operation.Avg)), fmt.Sprintf("%.2f%%", operation.ErrorRate)})
			}
			return writeTable(options.output, []string{"OPERATION", "COUNT", "SUM", "AVG", "ERROR"}, rows)
		},
	}
	command.Flags().Int64VarP(&environmentID, "environment", "e", 0, "environment ID")
	command.Flags().StringVarP(&namespace, "namespace", "n", "", "web, job, db, http, cache, or view")
	command.Flags().IntVar(&hours, "hours", 24, "time window")
	command.Flags().StringVar(&sort, "sort", "sum", "sort field")
	command.Flags().StringVar(&direction, "direction", "desc", "asc or desc")
	command.Flags().StringVar(&search, "search", "", "filter operations")
	command.Flags().IntVar(&limit, "limit", 25, "maximum operations")
	return command
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeTable(writer io.Writer, header []string, rows [][]string) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, strings.Join(header, "\t"))
	for _, row := range rows {
		fmt.Fprintln(table, strings.Join(row, "\t"))
	}
	return table.Flush()
}

func organizationRows(organizations []api.Organization) [][]string {
	rows := make([][]string, 0, len(organizations))
	for _, organization := range organizations {
		rows = append(rows, []string{fmt.Sprint(organization.ID), organization.Name})
	}
	return rows
}

func allOrganizations(ctx context.Context, client *api.Client) (api.OrganizationsResponse, error) {
	response := api.OrganizationsResponse{}
	query := url.Values{"limit": {"100"}}
	for {
		var page api.OrganizationsResponse
		if err := client.Get(ctx, "/api/v1/organizations", query, &page); err != nil {
			return response, err
		}
		response.Organizations = append(response.Organizations, page.Organizations...)
		if page.Pagination.Next == nil {
			break
		}
		query.Set("cursor", *page.Pagination.Next)
	}
	return response, nil
}

func allProjects(ctx context.Context, client *api.Client, query url.Values) (api.ProjectsResponse, error) {
	response := api.ProjectsResponse{}
	for {
		var page api.ProjectsResponse
		if err := client.Get(ctx, "/api/v1/projects", query, &page); err != nil {
			return response, err
		}
		response.Projects = append(response.Projects, page.Projects...)
		if page.Pagination.Next == nil {
			break
		}
		query.Set("cursor", *page.Pagination.Next)
	}
	return response, nil
}

func allEnvironments(ctx context.Context, client *api.Client, query url.Values) (api.EnvironmentsResponse, error) {
	response := api.EnvironmentsResponse{}
	for {
		var page api.EnvironmentsResponse
		if err := client.Get(ctx, "/api/v1/environments", query, &page); err != nil {
			return response, err
		}
		response.Environments = append(response.Environments, page.Environments...)
		if page.Pagination.Next == nil {
			break
		}
		query.Set("cursor", *page.Pagination.Next)
	}
	return response, nil
}

func formatMilliseconds(milliseconds int64) string {
	if milliseconds < 1_000 {
		return fmt.Sprintf("%dms", milliseconds)
	}
	return fmt.Sprintf("%.1fs", float64(milliseconds)/1_000)
}
