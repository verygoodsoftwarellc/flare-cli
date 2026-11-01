package command

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOverviewTableShowsTopOperations(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query = request.URL.RawQuery
		_, _ = writer.Write([]byte(`{"overviews":[{"environment":{"id":1,"project_id":1,"name":"Production"},"namespaces":[
			{"namespace":"web","impact":100,"count":10,"sum":500,"avg":50,"error_rate":0,"operations":[{"operation":"UsersController#index","impact":80,"count":4,"sum":400,"avg":100,"error_rate":25}]},
			{"namespace":"job","impact":0,"count":0,"sum":0,"avg":0,"error_rate":0,"operations":[]}
		]}]}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}}
	command := newOverviewCommand(options)
	command.SetArgs([]string{"-e", "1", "--sort", "count", "--limit", "1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(query, "sort=count") || !strings.Contains(query, "limit=1") {
		t.Fatalf("query = %s", query)
	}
	text := output.String()
	for _, want := range []string{"Production web: top operations by count", "UsersController#index", "25.00%", "80.0%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Production job: top") {
		t.Fatalf("empty namespace got a section:\n%s", text)
	}
}

func TestSeriesTableShowsWindowStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"environment":{"id":1,"project_id":1,"name":"Production"},"namespace":"job","hours":1,"bucket":"minute",
			"buckets":["2026-11-01T12:00:00Z","2026-11-01T12:01:00Z"],"data_through":"2026-11-01T12:01:00Z",
			"selector":{"target":"EventJob"},
			"stats":{"count":4,"sum":90000,"avg":22500,"error_count":1,"error_rate":25},
			"series":{"count":[3,1],"sum":[60000,30000],"error_count":[1,0]}}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}}
	command := newSeriesCommand(options)
	command.SetArgs([]string{"-e", "1", "-n", "job", "--target", "EventJob", "--hours", "1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	text := output.String()
	want := "Production job: 4 requests, 1.5m total, 22.5s avg, 25.00% errors (2 minute buckets, data through 2026-11-01T12:01:00Z)\n\nTIME"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("want prefix %q in:\n%s", want, text)
	}
	if !strings.Contains(text, "2026-11-01T12:01:00Z  1") {
		t.Fatalf("missing bucket row in:\n%s", text)
	}
}
