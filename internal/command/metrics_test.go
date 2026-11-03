package command

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	inZone(t, time.FixedZone("EST", -5*60*60))

	output := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: output, errors: &bytes.Buffer{}}
	command := newSeriesCommand(options)
	command.SetArgs([]string{"-e", "1", "-n", "job", "--target", "EventJob", "--hours", "1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	text := output.String()
	want := "Production job: 4 requests, 1.5m total, 22.5s avg, 25.00% errors (2 minute buckets, data through 2026-11-01 07:01)\n\nTIME"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("want prefix %q in:\n%s", want, text)
	}
	if !strings.Contains(text, "2026-11-01 07:01  1") {
		t.Fatalf("missing bucket row in:\n%s", text)
	}
}

// inZone sets the local time zone for one test.
func inZone(t *testing.T, zone *time.Location) {
	t.Helper()
	original := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = original })
}

func TestBucketTimesAreLocalLikeTheWebCharts(t *testing.T) {
	inZone(t, time.FixedZone("EST", -5*60*60))
	through := "2026-11-03T14:04:00Z"
	for _, test := range []struct{ got, want string }{
		{bucketTime("2026-11-03T14:02:00Z", "minute"), "2026-11-03 09:02"},
		{bucketTime("2026-11-03T03:00:00Z", "hour"), "2026-11-02 22:00"},
		{bucketTime("2026-11-03T00:00:00Z", "day"), "2026-11-03"},
		{bucketTime("not a time", "hour"), "not a time"},
		{dataThrough(&through, "minute"), "data through 2026-11-03 09:04"},
		{dataThrough(nil, "minute"), "no data"},
	} {
		if test.got != test.want {
			t.Errorf("got %q, want %q", test.got, test.want)
		}
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
