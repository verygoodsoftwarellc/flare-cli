package command

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

// The server's allowed values, shared by the CLI help text, the MCP tool
// schemas, and the MCP argument validation.
var (
	allowedHours = []int{1, 6, 24, 48, 168, 672}
	namespaces   = []string{"web", "job", "db", "http", "cache", "view"}
	dimensions   = []string{"namespace", "service", "target", "operation"}
	querySorts   = []string{"count", "sum", "avg", "error_rate"}
	sorts        = append(slices.Clone(querySorts), "impact")
	directions   = []string{"asc", "desc"}
)

const (
	maxFilterLength = 255
	maxQueryLimit   = 100
	maxSeriesLimit  = 25

	windowHelp    = "Windows of 1 or 6 hours use minute buckets, 24 to 168 hours use hour buckets, and 672 hours uses day buckets; hours 1 is a rolling 60 minute window ending now."
	seriesHelp    = "Every series is {count, sum, error_count} arrays aligned to the response's buckets array (per-bucket avg = sum/count); data_through is the latest bucket with data."
	dimensionHelp = "Dimension meanings by namespace: web — service: framework, target: controller#action, operation: HTTP status class (2xx..5xx); " +
		"job — service: backend (solid_queue, sidekiq), target: job class, operation: perform; db — service: adapter, target: table, operation: SQL verb; " +
		"http — service: remote host, target: method and path, operation: HTTP status class; cache — service: store, operation: read.hit/read.miss/write/delete; " +
		"view — service: renderer, target: template path, operation: template/partial/layout/collection."
)

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}

// scope is the environment, namespace, and window every metrics call takes.
// The same struct binds CLI flags and decodes MCP arguments.
type scope struct {
	EnvironmentID int64  `json:"environment_id"`
	Namespace     string `json:"namespace"`
	Hours         int    `json:"hours"`
}

func (s *scope) bindFlags(command *cobra.Command, namespaceHelp string) {
	command.Flags().Int64VarP(&s.EnvironmentID, "environment", "e", 0, "environment ID")
	if namespaceHelp != "" {
		command.Flags().StringVarP(&s.Namespace, "namespace", "n", "", namespaceHelp)
	}
	command.Flags().IntVar(&s.Hours, "hours", 24, "time window: "+joinInts(allowedHours))
}

// setDefaults fills the window the way the CLI flag default does, for MCP
// inputs that omit it.
func (s *scope) setDefaults() {
	if s.Hours == 0 {
		s.Hours = 24
	}
}

func (s scope) validate(namespaceRequired bool) error {
	if s.EnvironmentID <= 0 {
		return errors.New("environment is required")
	}
	if namespaceRequired && s.Namespace == "" {
		return errors.New("namespace is required")
	}
	if s.Namespace != "" && !slices.Contains(namespaces, s.Namespace) {
		return fmt.Errorf("namespace must be one of %s", strings.Join(namespaces, ", "))
	}
	if !slices.Contains(allowedHours, s.Hours) {
		return fmt.Errorf("hours must be one of %s", joinInts(allowedHours))
	}
	return nil
}

func (s scope) values() url.Values {
	query := url.Values{
		"environment_id": {strconv.FormatInt(s.EnvironmentID, 10)},
		"hours":          {strconv.Itoa(s.Hours)},
	}
	if s.Namespace != "" {
		query.Set("namespace", s.Namespace)
	}
	return query
}

// selector narrows to one operation by exact service / target / operation.
type selector struct {
	Service   string `json:"service"`
	Target    string `json:"target"`
	Operation string `json:"operation"`
}

func (s *selector) bindFlags(command *cobra.Command) {
	command.Flags().StringVar(&s.Service, "service", "", "filter to one service (e.g. rails, solid_queue, a host)")
	command.Flags().StringVar(&s.Target, "target", "", "filter to one target (e.g. UsersController#index, EventJob)")
	command.Flags().StringVar(&s.Operation, "operation", "", "filter to one operation (status class such as 2xx for web/http)")
}

func (s selector) validate() error {
	for name, value := range map[string]string{"service": s.Service, "target": s.Target, "operation": s.Operation} {
		if utf8.RuneCountInString(value) > maxFilterLength {
			return fmt.Errorf("%s accepts at most %d characters", name, maxFilterLength)
		}
	}
	return nil
}

func (s selector) apply(query url.Values) {
	for name, value := range map[string]string{"service": s.Service, "target": s.Target, "operation": s.Operation} {
		if value != "" {
			query.Set(name, value)
		}
	}
}

func selectorValue(s api.Selector, dimension string) string {
	switch dimension {
	case "namespace":
		return s.Namespace
	case "service":
		return s.Service
	case "target":
		return s.Target
	case "operation":
		return s.Operation
	}
	return ""
}

// --- table rendering shared by every metrics command ---

var statsHeader = []string{"COUNT", "SUM", "AVG", "ERROR"}

func statsCells(count, sum int64, avg, errorRate float64) []string {
	return []string{fmt.Sprint(count), formatSum(sum), formatMilliseconds(avg), fmt.Sprintf("%.2f%%", errorRate)}
}

func impactCell(impact *float64) string {
	if impact == nil {
		return ""
	}
	return fmt.Sprintf("%.1f%%", *impact)
}

func rankedRows(operations []api.RankedOperation) [][]string {
	rows := make([][]string, 0, len(operations))
	for _, operation := range operations {
		rows = append(rows, append(append([]string{operation.Operation}, statsCells(operation.Count, operation.Sum, operation.Avg, operation.ErrorRate)...), impactCell(operation.Impact)))
	}
	return rows
}

// seriesRows renders bucket-aligned arrays one bucket per row, deriving avg
// and error rate the same way the server does for window totals.
func seriesRows(buckets []string, series api.Series) [][]string {
	rows := make([][]string, 0, len(buckets))
	for index, bucket := range buckets {
		count, sum, errorCount := series.Count[index], series.Sum[index], series.ErrorCount[index]
		avg, errorRate := 0.0, 0.0
		if count > 0 {
			avg = float64(sum) / float64(count)
			errorRate = float64(errorCount) / float64(count) * 100
		}
		rows = append(rows, append([]string{bucket}, statsCells(count, sum, avg, errorRate)...))
	}
	return rows
}

func dataThrough(value *string) string {
	if value == nil {
		return "no data"
	}
	return *value
}

// --- commands ---

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
					rows = append(rows, append([]string{overview.Environment.Name, namespace.Namespace}, statsCells(namespace.Count, namespace.Sum, namespace.Avg, namespace.ErrorRate)...))
				}
			}
			return writeTable(options.output, append([]string{"ENVIRONMENT", "NAMESPACE"}, statsHeader...), rows)
		},
	}
	command.Flags().Int64SliceVarP(&environmentIDs, "environment", "e", nil, "environment ID (repeatable)")
	command.Flags().IntVar(&hours, "hours", 24, "time window: "+joinInts(allowedHours))
	command.Flags().StringVar(&sort, "sort", "sum", "rank operations by "+strings.Join(sorts, ", "))
	command.Flags().IntVar(&limit, "limit", 5, "top operations per namespace")
	return command
}

func newNamespaceCommand(options *rootOptions) *cobra.Command {
	var scope scope
	var limit int
	var sort, direction, search string
	command := &cobra.Command{
		Use:   "namespace",
		Short: "List operations in a namespace",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := scope.validate(true); err != nil {
				return err
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			query := scope.values()
			query.Set("sort", sort)
			query.Set("direction", direction)
			query.Set("limit", strconv.Itoa(limit))
			query.Set("q", search)
			var response api.NamespaceResponse
			if err := client.Get(command.Context(), "/api/v1/namespaces", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			rows := make([][]string, 0, len(response.Operations))
			for _, operation := range response.Operations {
				rows = append(rows, append([]string{operation.Operation}, statsCells(operation.Count, operation.Sum, operation.Avg, operation.ErrorRate)...))
			}
			return writeTable(options.output, append([]string{"OPERATION"}, statsHeader...), rows)
		},
	}
	scope.bindFlags(command, strings.Join(namespaces, ", "))
	command.Flags().StringVar(&sort, "sort", "sum", "sort field: "+strings.Join(sorts, ", "))
	command.Flags().StringVar(&direction, "direction", "desc", "asc or desc")
	command.Flags().StringVar(&search, "search", "", "filter operations")
	command.Flags().IntVar(&limit, "limit", 25, "maximum operations")
	return command
}

func newNamespaceOverviewCommand(options *rootOptions) *cobra.Command {
	var scope scope
	var limit int
	command := &cobra.Command{
		Use:   "namespace-overview",
		Short: "Namespace stats, series, and top operations with series",
		Long: "Mirror of the Flare namespace page: stats and a time series for the namespace plus the top " +
			"operations by sum and by count, each with its own series. " + windowHelp + " The table output shows " +
			"the rankings; use --json for the bucket-aligned series arrays.",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := scope.validate(true); err != nil {
				return err
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			query := scope.values()
			query.Set("limit", strconv.Itoa(limit))
			var response api.NamespaceOverviewResponse
			if err := client.Get(command.Context(), "/api/v1/namespaces/overview", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			fmt.Fprintf(options.output, "%s %s: %d requests, %s total, %s avg, %.2f%% errors, %s of environment time (%d %s buckets, data through %s)\n\n",
				response.Environment.Name, response.Namespace, response.Stats.Count, formatSum(response.Stats.Sum),
				formatMilliseconds(response.Stats.Avg), response.Stats.ErrorRate, impactCell(response.Stats.Impact),
				len(response.Buckets), response.Bucket, dataThrough(response.DataThrough))
			header := append(append([]string{"OPERATION"}, statsHeader...), "IMPACT")
			fmt.Fprintln(options.output, "Top by sum")
			if err := writeTable(options.output, header, rankedRows(response.TopBySum)); err != nil {
				return err
			}
			fmt.Fprintln(options.output, "\nTop by count")
			return writeTable(options.output, header, rankedRows(response.TopByCount))
		},
	}
	scope.bindFlags(command, strings.Join(namespaces, ", "))
	command.Flags().IntVar(&limit, "limit", 10, fmt.Sprintf("top operations per ranking (1-%d)", maxSeriesLimit))
	return command
}

func newSeriesCommand(options *rootOptions) *cobra.Command {
	var scope scope
	var selector selector
	command := &cobra.Command{
		Use:   "series",
		Short: "Time series for a namespace or one operation",
		Long: "Mirror of the Flare operation page: stats and one row per bucket (count, sum, avg, error rate). " +
			windowHelp + " Narrow to one operation with the selector fields shown by `flare metrics namespace --json`.",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := errors.Join(scope.validate(true), selector.validate()); err != nil {
				return err
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			query := scope.values()
			selector.apply(query)
			var response api.OperationSeriesResponse
			if err := client.Get(command.Context(), "/api/v1/series", query, &response); err != nil {
				return err
			}
			if options.json {
				return writeJSON(options.output, response)
			}
			return writeTable(options.output, append([]string{"TIME"}, statsHeader...), seriesRows(response.Buckets, response.Series))
		},
	}
	scope.bindFlags(command, strings.Join(namespaces, ", "))
	selector.bindFlags(command)
	return command
}

func newQueryCommand(options *rootOptions) *cobra.Command {
	var scope scope
	var selector selector
	var limit, minCount int
	var sort, direction string
	var groupBy []string
	var series bool
	command := &cobra.Command{
		Use:   "query",
		Short: "Generic metrics query: filter, group, rank, optionally with series",
		Long: "Filter on any of namespace, service, target, and operation; group by any subset of them; rank the " +
			"groups by " + strings.Join(querySorts, ", ") + "; and optionally attach a series to the total and to each " +
			"group. The other metrics commands are presets of this one. " + windowHelp + " " + dimensionHelp,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := errors.Join(scope.validate(false), selector.validate()); err != nil {
				return err
			}
			if minCount < 0 {
				return errors.New("min-count must be 0 or more")
			}
			client, err := options.client()
			if err != nil {
				return err
			}
			query := scope.values()
			selector.apply(query)
			query.Set("sort", sort)
			query.Set("direction", direction)
			query.Set("limit", strconv.Itoa(limit))
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
				response.Environment.Name, response.Total.Count, formatSum(response.Total.Sum),
				formatMilliseconds(response.Total.Avg), response.Total.ErrorRate, dataThrough(response.DataThrough))
			if len(response.Groups) == 0 {
				return nil
			}
			fmt.Fprintln(options.output)
			header := []string{"LABEL"}
			for _, dimension := range response.GroupBy {
				header = append(header, strings.ToUpper(dimension))
			}
			header = append(append(header, statsHeader...), "IMPACT")
			rows := make([][]string, 0, len(response.Groups))
			for _, group := range response.Groups {
				row := []string{group.Label}
				for _, dimension := range response.GroupBy {
					row = append(row, selectorValue(group.Selector, dimension))
				}
				rows = append(rows, append(append(row, statsCells(group.Count, group.Sum, group.Avg, group.ErrorRate)...), impactCell(group.Impact)))
			}
			return writeTable(options.output, header, rows)
		},
	}
	scope.bindFlags(command, "filter: "+strings.Join(namespaces, ", "))
	selector.bindFlags(command)
	command.Flags().StringSliceVar(&groupBy, "group-by", nil, "group by dimensions: "+strings.Join(dimensions, ", ")+" (repeatable or comma separated)")
	command.Flags().StringVar(&sort, "sort", "sum", "rank groups by "+strings.Join(querySorts, ", "))
	command.Flags().StringVar(&direction, "direction", "desc", "asc or desc")
	command.Flags().IntVar(&limit, "limit", 10, fmt.Sprintf("groups to return (1-%d, or 1-%d with --series)", maxQueryLimit, maxSeriesLimit))
	command.Flags().BoolVar(&series, "series", false, "attach a bucketed series to the total and each group (JSON output)")
	command.Flags().IntVar(&minCount, "min-count", 0, "ignore groups with fewer requests (useful with --sort error_rate)")
	return command
}
