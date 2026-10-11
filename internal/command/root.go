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
	parent.AddCommand(newOverviewCommand(options), newNamespaceCommand(options), newNamespaceOverviewCommand(options), newSeriesCommand(options), newQueryCommand(options))
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

func newNamespaceOverviewCommand(options *rootOptions) *cobra.Command {
	var environmentID int64
	var hours, limit int
	var namespace string
	command := &cobra.Command{
		Use:   "namespace-overview",
		Short: "Namespace stats, series, and top operations with series",
		Long: "Mirror of the Flare namespace page: stats and a time series for the namespace plus the top " +
			"operations by sum and by count, each with its own series. Windows of 1 or 6 hours use minute " +
			"buckets, 24 to 168 hours use hour buckets, and 672 hours uses day buckets. The table output shows " +
			"the rankings; use --json for the bucket-aligned series arrays.",
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
				"hours": {strconv.Itoa(hours)}, "limit": {strconv.Itoa(limit)},
			}
			var response api.NamespaceOverviewResponse
			if err := client.Get(command.Context(), "/api/v1/namespaces/overview", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			fmt.Fprintf(options.output, "%s %s: %d requests, %s total, %s avg, %.2f%% errors, %.1f%% of environment time (%d %s buckets, data through %s)\n\n",
				response.Environment.Name, response.Namespace, response.Stats.Count, formatMilliseconds(response.Stats.Sum),
				formatMilliseconds(int64(response.Stats.Avg)), response.Stats.ErrorRate, response.Stats.Impact,
				len(response.Buckets), response.Bucket, dataThrough(response.DataThrough))
			fmt.Fprintln(options.output, "Top by sum")
			if err := writeTable(options.output, []string{"OPERATION", "COUNT", "SUM", "AVG", "ERROR", "IMPACT"}, rankedRows(response.TopBySum)); err != nil {
				return err
			}
			fmt.Fprintln(options.output, "\nTop by count")
			return writeTable(options.output, []string{"OPERATION", "COUNT", "SUM", "AVG", "ERROR", "IMPACT"}, rankedRows(response.TopByCount))
		},
	}
	command.Flags().Int64VarP(&environmentID, "environment", "e", 0, "environment ID")
	command.Flags().StringVarP(&namespace, "namespace", "n", "", "web, job, db, http, cache, or view")
	command.Flags().IntVar(&hours, "hours", 24, "time window: 1, 6, 24, 48, 168, or 672")
	command.Flags().IntVar(&limit, "limit", 10, "top operations per ranking (1-25)")
	return command
}

func rankedRows(operations []api.RankedOperation) [][]string {
	rows := make([][]string, 0, len(operations))
	for _, operation := range operations {
		rows = append(rows, []string{operation.Operation, fmt.Sprint(operation.Count), formatMilliseconds(operation.Sum), formatMilliseconds(int64(operation.Avg)), fmt.Sprintf("%.2f%%", operation.ErrorRate), fmt.Sprintf("%.1f%%", operation.Impact)})
	}
	return rows
}

func dataThrough(value *string) string {
	if value == nil {
		return "no data"
	}
	return *value
}

func newSeriesCommand(options *rootOptions) *cobra.Command {
	var environmentID int64
	var hours int
	var namespace, service, target, operation string
	command := &cobra.Command{
		Use:   "series",
		Short: "Time series for a namespace or one operation",
		Long: "Mirror of the Flare operation page: stats and one row per bucket (count, sum, avg, error rate). " +
			"Windows of 1 or 6 hours use minute buckets, 24 to 168 hours use hour buckets, and 672 hours uses " +
			"day buckets. Narrow to one operation with the selector fields shown by `flare metrics namespace --json`.",
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
				"hours": {strconv.Itoa(hours)},
			}
			addSelector(query, service, target, operation)
			var response api.OperationSeriesResponse
			if err := client.Get(command.Context(), "/api/v1/series", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			return writeTable(options.output, []string{"TIME", "COUNT", "SUM", "AVG", "ERROR"}, seriesRows(response.Buckets, response.Series))
		},
	}
	command.Flags().Int64VarP(&environmentID, "environment", "e", 0, "environment ID")
	command.Flags().StringVarP(&namespace, "namespace", "n", "", "web, job, db, http, cache, or view")
	command.Flags().IntVar(&hours, "hours", 24, "time window: 1, 6, 24, 48, 168, or 672")
	command.Flags().StringVar(&service, "service", "", "filter to one service (e.g. rails, solid_queue, a host)")
	command.Flags().StringVar(&target, "target", "", "filter to one target (e.g. UsersController#index, EventJob)")
	command.Flags().StringVar(&operation, "operation", "", "filter to one operation (status class such as 2xx for web/http)")
	return command
}

// seriesRows renders bucket-aligned arrays one bucket per row, deriving avg
// and error rate the same way the server does for window totals.
func seriesRows(buckets []string, series api.Series) [][]string {
	rows := make([][]string, 0, len(buckets))
	for index, bucket := range buckets {
		var count, sum, errorCount int64
		if index < len(series.Count) {
			count = series.Count[index]
		}
		if index < len(series.Sum) {
			sum = series.Sum[index]
		}
		if index < len(series.ErrorCount) {
			errorCount = series.ErrorCount[index]
		}
		avg, errorRate := int64(0), 0.0
		if count > 0 {
			avg = sum / count
			errorRate = float64(errorCount) / float64(count) * 100
		}
		rows = append(rows, []string{bucket, fmt.Sprint(count), formatMilliseconds(sum), formatMilliseconds(avg), fmt.Sprintf("%.2f%%", errorRate)})
	}
	return rows
}

func newQueryCommand(options *rootOptions) *cobra.Command {
	var environmentID int64
	var hours, limit, minCount int
	var namespace, service, target, operation, sort, direction string
	var groupBy []string
	var series bool
	command := &cobra.Command{
		Use:   "query",
		Short: "Generic metrics query: filter, group, rank, optionally with series",
		Long: "Filter on any of namespace, service, target, and operation; group by any subset of them; rank the " +
			"groups by count, sum, avg, or error_rate; and optionally attach a series to the total and to each " +
			"group. The other metrics commands are presets of this one. Dimension meanings differ by namespace " +
			"(web: target is controller#action and operation is the HTTP status class; http: service is the remote " +
			"host; db: target is the table and operation the SQL verb; job: target is the job class).",
		RunE: func(command *cobra.Command, _ []string) error {
			if environmentID == 0 {
				return errors.New("--environment is required")
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			query := url.Values{
				"environment_id": {strconv.FormatInt(environmentID, 10)},
				"hours":          {strconv.Itoa(hours)},
				"sort":           {sort},
				"direction":      {direction},
				"limit":          {strconv.Itoa(limit)},
			}
			if namespace != "" {
				query.Set("namespace", namespace)
			}
			addSelector(query, service, target, operation)
			if len(groupBy) > 0 {
				query.Set("group_by", strings.Join(groupBy, ","))
			}
			if series {
				query.Set("series", "true")
			}
			if minCount > 0 {
				query.Set("min_count", strconv.Itoa(minCount))
			}
			var response api.QueryResponse
			if err := client.Get(command.Context(), "/api/v1/metrics/query", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			fmt.Fprintf(options.output, "%s: %d requests, %s total, %s avg, %.2f%% errors (data through %s)\n",
				response.Environment.Name, response.Total.Count, formatMilliseconds(response.Total.Sum),
				formatMilliseconds(int64(response.Total.Avg)), response.Total.ErrorRate, dataThrough(response.DataThrough))
			if len(response.Groups) == 0 {
				return nil
			}
			fmt.Fprintln(options.output)
			header := []string{"LABEL"}
			for _, dimension := range response.GroupBy {
				header = append(header, strings.ToUpper(dimension))
			}
			header = append(header, "COUNT", "SUM", "AVG", "ERROR", "IMPACT")
			rows := make([][]string, 0, len(response.Groups))
			for _, group := range response.Groups {
				row := []string{group.Label}
				for _, dimension := range response.GroupBy {
					row = append(row, selectorValue(group.Selector, dimension))
				}
				row = append(row, fmt.Sprint(group.Count), formatMilliseconds(group.Sum), formatMilliseconds(int64(group.Avg)), fmt.Sprintf("%.2f%%", group.ErrorRate), fmt.Sprintf("%.1f%%", group.Impact))
				rows = append(rows, row)
			}
			return writeTable(options.output, header, rows)
		},
	}
	command.Flags().Int64VarP(&environmentID, "environment", "e", 0, "environment ID")
	command.Flags().IntVar(&hours, "hours", 24, "time window: 1, 6, 24, 48, 168, or 672")
	command.Flags().StringVarP(&namespace, "namespace", "n", "", "filter: web, job, db, http, cache, or view")
	command.Flags().StringVar(&service, "service", "", "filter to one service")
	command.Flags().StringVar(&target, "target", "", "filter to one target")
	command.Flags().StringVar(&operation, "operation", "", "filter to one operation")
	command.Flags().StringSliceVar(&groupBy, "group-by", nil, "group by dimensions: namespace, service, target, operation (repeatable or comma separated)")
	command.Flags().StringVar(&sort, "sort", "sum", "rank groups by count, sum, avg, or error_rate")
	command.Flags().StringVar(&direction, "direction", "desc", "asc or desc")
	command.Flags().IntVar(&limit, "limit", 10, "groups to return (1-100, or 1-25 with --series)")
	command.Flags().BoolVar(&series, "series", false, "attach a bucketed series to the total and each group (JSON output)")
	command.Flags().IntVar(&minCount, "min-count", 0, "ignore groups with fewer requests (useful with --sort error_rate)")
	return command
}

func selectorValue(selector api.Selector, dimension string) string {
	switch dimension {
	case "namespace":
		return selector.Namespace
	case "service":
		return selector.Service
	case "target":
		return selector.Target
	case "operation":
		return selector.Operation
	}
	return ""
}

func addSelector(query url.Values, service, target, operation string) {
	if service != "" {
		query.Set("service", service)
	}
	if target != "" {
		query.Set("target", target)
	}
	if operation != "" {
		query.Set("operation", operation)
	}
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
