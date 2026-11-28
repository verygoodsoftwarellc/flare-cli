package command

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

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
	want := "Production job: 4 requests, 1.5m total, 22.5s avg, 25.00% errors (2 minute buckets, data through 2026-11-01 07:01 EST)\n\nTIME"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("want prefix %q in:\n%s", want, text)
	}
	if !strings.Contains(text, "2026-11-01 07:01 EST  1") {
		t.Fatalf("missing bucket row in:\n%s", text)
	}
}

func TestBucketTimesAreLocalLikeTheWebCharts(t *testing.T) {
	through := "2026-11-03T14:04:00Z"
	for _, test := range []struct{ got, want string }{
		{bucketTime("2026-11-03T14:02:00Z", "minute"), "2026-11-03 09:02 EST"},
		{bucketTime("2026-11-03T03:00:00Z", "hour"), "2026-11-02 22:00 EST"},
		{bucketTime("2026-11-03T00:00:00Z", "day"), "2026-11-03"},
		{bucketTime("not a time", "hour"), "not a time"},
		{dataThrough(&through, "minute"), "data through 2026-11-03 09:04 EST"},
		{dataThrough(nil, "minute"), "no data"},
	} {
		if test.got != test.want {
			t.Errorf("got %q, want %q", test.got, test.want)
		}
	}
}

func TestBucketTimesStayDistinctWhenClocksFallBack(t *testing.T) {
	first, second := bucketTime("2026-11-01T05:00:00Z", "hour"), bucketTime("2026-11-01T06:00:00Z", "hour")
	if first != "2026-11-01 01:00 EDT" || second != "2026-11-01 01:00 EST" {
		t.Fatalf("got %q and %q", first, second)
	}
}

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

func TestNamespaceTableShowsImpact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"environment":{"id":1,"project_id":1,"name":"Production"},"namespace":"web","operations":[
			{"operation":"UsersController#index","impact":42.5,"count":4,"sum":400,"avg":100,"error_rate":25}
		],"pagination":{"next":null,"previous":null}}`))
	}))
	defer server.Close()
	t.Setenv("FLARE_TOKEN", "flare_pat_test")

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}}
	command := newNamespaceCommand(options)
	command.SetArgs([]string{"-e", "1", "-n", "web", "--sort", "impact"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"IMPACT", "42.5%"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}
}

func TestMetricsCommandsTakeTheEnvironmentInThePath(t *testing.T) {
	t.Setenv("FLARE_TOKEN", "flare_pat_test")
	var path string
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path, query = request.URL.Path, request.URL.Query()
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	for _, test := range []struct {
		build     func(*rootOptions) *cobra.Command
		args      []string
		path      string
		namespace string
	}{
		{newNamespaceCommand, []string{"-e", "7", "-n", "web"}, "/api/v1/environments/7/namespaces/web/operations", ""},
		{newNamespaceOverviewCommand, []string{"-e", "7", "-n", "job"}, "/api/v1/environments/7/namespaces/job", ""},
		{newSeriesCommand, []string{"-e", "7", "-n", "db", "--target", "users"}, "/api/v1/environments/7/namespaces/db/series", ""},
		{newQueryCommand, []string{"-e", "7", "-n", "web"}, "/api/v1/environments/7/metrics", "web"},
		{newQueryCommand, []string{"-e", "7"}, "/api/v1/environments/7/metrics", ""},
	} {
		command := test.build(&rootOptions{name: "flare-cli", apiURL: server.URL, json: true, output: &bytes.Buffer{}, errors: &bytes.Buffer{}})
		command.SetArgs(test.args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if path != test.path {
			t.Errorf("%v: path %s, want %s", test.args, path, test.path)
		}
		if query.Has("environment_id") || query.Get("namespace") != test.namespace {
			t.Errorf("%v: query %s, want no environment_id and namespace %q", test.args, query.Encode(), test.namespace)
		}
	}
	if query.Get("hours") != "24" {
		t.Errorf("hours = %q, want 24", query.Get("hours"))
	}
}
