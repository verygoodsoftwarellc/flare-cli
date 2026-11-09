package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/verygoodsoftwarellc/flare-cli/internal/api"
)

func TestCatalogPaginationRejectsRepeatedCursors(t *testing.T) {
	for _, catalog := range []struct {
		name string
		load func(context.Context, *api.Client) error
	}{
		{"organizations", func(ctx context.Context, c *api.Client) error { _, err := allOrganizations(ctx, c); return err }},
		{"projects", func(ctx context.Context, c *api.Client) error {
			_, err := allProjects(ctx, c, url.Values{"limit": {"100"}})
			return err
		}},
		{"environments", func(ctx context.Context, c *api.Client) error {
			_, err := allEnvironments(ctx, c, url.Values{"limit": {"100"}})
			return err
		}},
	} {
		for _, cursors := range [][]string{{"same", "same"}, {"one", "two", "one"}, {""}} {
			t.Run(catalog.name+"/"+strings.Join(cursors, ","), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if requests > len(cursors) {
						http.Error(w, "unexpected repeated request", http.StatusInternalServerError)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{catalog.name: []any{}, "pagination": map[string]any{"next": cursors[requests-1]}})
				}))
				defer server.Close()
				err := catalog.load(context.Background(), api.New(server.URL, "flare_pat_test"))
				if err == nil || !strings.Contains(err.Error(), "pagination cursor repeated") {
					t.Fatalf("expected pagination error, got %v after %d requests", err, requests)
				}
				if requests != len(cursors) {
					t.Fatalf("requests=%d,want %d", requests, len(cursors))
				}
			})
		}
	}
}

func TestCatalogCommandsReturnPaginationErrors(t *testing.T) {
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	for _, name := range []string{"org", "project", "environment"} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests > 2 {
					http.Error(w, "unexpected repeated request", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"pagination": map[string]any{"next": "repeat"}})
			}))
			defer server.Close()
			output := &bytes.Buffer{}
			options := &rootOptions{apiURL: server.URL, json: true, output: output, errors: io.Discard}
			var command *cobra.Command
			switch name {
			case "org":
				command = newOrganizationCommand(options)
			case "project":
				command = newProjectCommand(options)
			case "environment":
				command = newEnvironmentCommand(options)
			}
			command.SilenceUsage, command.SilenceErrors = true, true
			command.SetArgs([]string{"list"})
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), "pagination cursor repeated") {
				t.Fatalf("err=%v", err)
			}
			if requests != 2 {
				t.Fatalf("requests=%d,want2", requests)
			}
			if output.Len() != 0 {
				t.Fatalf("error printed partial JSON: %s", output)
			}
			t.Logf("%s list --json: %v; %d requests; %d stdout bytes", name, err, requests, output.Len())
		})
	}
}
