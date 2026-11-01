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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
	"github.com/verygoodsoftwarellc/flare-cli/internal/auth"
	"github.com/verygoodsoftwarellc/flare-cli/internal/config"
	"github.com/verygoodsoftwarellc/flare-cli/internal/version"
)

type rootOptions struct {
	name    string
	apiURL  string
	json    bool
	verbose bool
	output  io.Writer
	errors  io.Writer
	input   io.Reader
}

func Execute(name string) error {
	return newRootCommand(name).Execute()
}

func newRootCommand(name string) *cobra.Command {
	options := &rootOptions{name: name, output: os.Stdout, errors: os.Stderr, input: os.Stdin}
	command := &cobra.Command{
		Use:           name,
		Short:         "Inspect application performance in Flare",
		Version:       version.Current(),
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
		return nil, options.authenticationError(err)
	}
	client := api.New(settings.APIURL, token)
	client.Verbose = options.verbose
	client.ErrWriter = options.errors
	login := options.loginCommand()
	if os.Getenv("FLARE_TOKEN") != "" {
		client.AuthHint = fmt.Sprintf("FLARE_TOKEN was rejected; replace it, or unset it and run %s.", login)
	} else {
		client.AuthHint = fmt.Sprintf("Run %s to sign in again.", login)
	}
	return client, nil
}

func (options *rootOptions) authenticationError(err error) error {
	return fmt.Errorf("%w; run %s or set FLARE_TOKEN", err, options.loginCommand())
}

// loginCommand names the sign-in command to suggest. A one-off --api-url is
// mentioned in words rather than echoed, so there is nothing to shell-quote.
func (options *rootOptions) loginCommand() string {
	login := "`" + options.name + " auth login`"
	if options.apiURL != "" {
		login += " with the same --api-url"
	}
	return login
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
				result, err := auth.Login(ctx, settings.APIURL, nil, options.errors)
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
			client, err := options.client()
			if err != nil {
				return err
			}
			if err := verifyAuthentication(command.Context(), client); err != nil {
				return err
			}
			fmt.Fprintf(options.output, "Authenticated with %s\n", client.BaseURL)
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
			storeSkipped, err := settings.DeleteToken()
			if err != nil {
				return err
			}
			if storeSkipped {
				fmt.Fprintln(options.errors, "warning: OS credential store unavailable; any token saved there was not removed")
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
			organizations, err := allOrganizations(command.Context(), client)
			if err != nil {
				return err
			}
			return writeTable(options.output, []string{"ID", "NAME", "ORGANIZATION"}, projectRows(response.Projects, organizations.Organizations))
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
			projects, err := allProjects(command.Context(), client, url.Values{"limit": {"100"}})
			if err != nil {
				return err
			}
			organizations, err := allOrganizations(command.Context(), client)
			if err != nil {
				return err
			}
			return writeTable(options.output, []string{"ID", "NAME", "PROJECT", "ORGANIZATION"}, environmentRows(response.Environments, projects.Projects, organizations.Organizations))
		},
	}
	list.Flags().Int64Var(&projectID, "project", 0, "filter by project ID")
	parent.AddCommand(list)
	return parent
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

func projectRows(projects []api.Project, organizations []api.Organization) [][]string {
	organizationNames := make(map[int64]string, len(organizations))
	for _, organization := range organizations {
		organizationNames[organization.ID] = organization.Name
	}

	rows := make([][]string, 0, len(projects))
	for _, project := range projects {
		organizationName := nameOrID(organizationNames[project.OrganizationID], project.OrganizationID)
		rows = append(rows, []string{fmt.Sprint(project.ID), project.Name, organizationName})
	}
	return rows
}

func environmentRows(environments []api.Environment, projects []api.Project, organizations []api.Organization) [][]string {
	organizationNames := make(map[int64]string, len(organizations))
	for _, organization := range organizations {
		organizationNames[organization.ID] = organization.Name
	}
	projectsByID := make(map[int64]api.Project, len(projects))
	for _, project := range projects {
		projectsByID[project.ID] = project
	}

	rows := make([][]string, 0, len(environments))
	for _, environment := range environments {
		project, found := projectsByID[environment.ProjectID]
		projectName := nameOrID(project.Name, environment.ProjectID)
		organizationName := "-"
		if found {
			organizationName = nameOrID(organizationNames[project.OrganizationID], project.OrganizationID)
		}
		rows = append(rows, []string{fmt.Sprint(environment.ID), environment.Name, projectName, organizationName})
	}
	return rows
}

func nameOrID(name string, id int64) string {
	if name != "" {
		return name
	}
	return fmt.Sprint(id)
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

// formatMilliseconds and formatSum match how the Flare web pages show an
// average and a total.
func formatMilliseconds(milliseconds float64) string {
	if milliseconds < 1_000 {
		return fmt.Sprintf("%.0fms", milliseconds)
	}
	return fmt.Sprintf("%.1fs", milliseconds/1_000)
}

func formatSum(milliseconds int64) string {
	switch {
	case milliseconds < 60_000:
		return formatMilliseconds(float64(milliseconds))
	case milliseconds < 3_600_000:
		return fmt.Sprintf("%.1fm", float64(milliseconds)/60_000)
	}
	return fmt.Sprintf("%.1fh", float64(milliseconds)/3_600_000)
}
